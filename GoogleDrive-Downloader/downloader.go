package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// ---------- НАСТРОЙКИ ----------

// Если карта непустая — скачиваются ТОЛЬКО файлы с этими расширениями.
// Пустая карта = скачивать всё.
var allowedExtensions = map[string]bool{
	".sto": true,
	// ".zip": true,   // раскомментируйте при необходимости
}

// Максимальное число попыток для одного файла.
const maxAttempts = 6

// ---------- ФИЛЬТР ----------

func shouldDownload(name string) bool {
	if len(allowedExtensions) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	return allowedExtensions[ext]
}

// ---------- ОБХОД ПАПКИ ----------

func downloadFolder(ctx context.Context, srv *drive.Service, folderID, localPath string) error {
	query := fmt.Sprintf("'%s' in parents and trashed=false", folderID)

	var allFiles []*drive.File
	pageToken := ""
	for {
		call := srv.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, mimeType, size)").
			PageSize(1000)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		res, err := call.Do()
		if err != nil {
			return fmt.Errorf("не удалось получить список файлов для папки %s: %w", folderID, err)
		}
		allFiles = append(allFiles, res.Files...)
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}

	if len(allFiles) == 0 {
		return nil
	}

	if err := os.MkdirAll(localPath, os.ModePerm); err != nil {
		return fmt.Errorf("не удалось создать локальную папку %s: %w", localPath, err)
	}

	for _, file := range allFiles {
		filePath := filepath.Join(localPath, file.Name)

		if file.MimeType == "application/vnd.google-apps.folder" {
			log.Printf("📁 Вход в папку: %s", filePath)
			if err := downloadFolder(ctx, srv, file.Id, filePath); err != nil {
				return err
			}
			continue
		}

		// Google Docs/Sheets/Slides в нативном формате нельзя скачать через get_media
		if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.") {
			log.Printf("⏭️  Пропуск Google-документа: %s (%s)", filePath, file.MimeType)
			continue
		}

		if !shouldDownload(file.Name) {
			continue
		}

		var expectedSize int64
		if file.Size != 0 {
			expectedSize = file.Size
		}

		log.Printf("⬇️  Скачивание: %s (ожидаемый размер: %d байт)", filePath, expectedSize)
		if err := downloadFileWithRetry(ctx, srv, file.Id, filePath, expectedSize); err != nil {
			return err
		}
	}
	return nil
}

// ---------- RETRY ----------

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case http.StatusForbidden,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		}
	}
	// Сетевые ошибки тоже повторяем
	if strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "timeout") ||
		strings.Contains(err.Error(), "EOF") {
		return true
	}
	return false
}

func downloadFileWithRetry(ctx context.Context, srv *drive.Service, fileID, filePath string, expectedSize int64) error {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := downloadFile(ctx, srv, fileID, filePath, expectedSize)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isRetryable(err) {
			return err
		}

		// Экспоненциальная задержка: 2s, 4s, 8s, 16s, 32s, максимум 60s
		wait := time.Duration(1<<uint(attempt)) * time.Second
		if wait > 60*time.Second {
			wait = 60 * time.Second
		}
		log.Printf("⚠️  Попытка %d/%d не удалась (%v). Ждём %v...", attempt, maxAttempts, err, wait)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("после %d попыток не удалось скачать %s: %w", maxAttempts, filePath, lastErr)
}

// ---------- СКАЧИВАНИЕ ФАЙЛА С ДОКАЧКОЙ ----------

func downloadFile(ctx context.Context, srv *drive.Service, fileID, filePath string, expectedSize int64) error {
	var localSize int64

	// Проверяем, есть ли уже локальная копия.
	if info, err := os.Stat(filePath); err == nil {
		localSize = info.Size()
		// Если размер совпадает с ожидаемым — файл уже скачан, выходим.
		if expectedSize > 0 && localSize == expectedSize {
			log.Printf("✅ Уже скачан, пропуск: %s", filePath)
			return nil
		}
		if expectedSize > 0 && localSize > expectedSize {
			// Странно: локальный файл больше. Перекачиваем с нуля.
			log.Printf("⚠️  Локальный файл больше ожидаемого, перекачиваю: %s", filePath)
			localSize = 0
			_ = os.Remove(filePath)
		}
	}

	call := srv.Files.Get(fileID)
	if localSize > 0 {
		log.Printf("↻ Докачиваю с %d байт: %s", localSize, filePath)
		call.Header().Set("Range", fmt.Sprintf("bytes=%d-", localSize))
	}

	resp, err := call.Download()
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var out *os.File
	if localSize > 0 && resp.StatusCode == http.StatusPartialContent {
		// Сервер подтвердил, что отдаёт остаток — открываем на дозапись.
		out, err = os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		// 200 OK — сервер проигнорировал Range, качаем с нуля.
		out, err = os.Create(filePath)
		localSize = 0
	}
	if err != nil {
		return fmt.Errorf("не удалось открыть файл %s: %w", filePath, err)
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка записи файла %s: %w", filePath, err)
	}

	total := localSize + written
	if expectedSize > 0 && total != expectedSize {
		return fmt.Errorf("размер файла %s не совпал: скачано %d, ожидалось %d", filePath, total, expectedSize)
	}

	log.Printf("✅ Скачан: %s (%d байт)", filePath, total)
	return nil
}

// ---------- MAIN ----------

func main() {
	ctx := context.Background()

	b, err := os.ReadFile("service-account.json")
	if err != nil {
		log.Fatalf("Не удалось прочитать service-account.json: %v", err)
	}

	config, err := google.JWTConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		log.Fatalf("Не удалось создать JWT-конфигурацию: %v", err)
	}

	srv, err := drive.NewService(ctx, option.WithHTTPClient(config.Client(ctx)))
	if err != nil {
		log.Fatalf("Не удалось создать сервис Drive: %v", err)
	}

	folderID := "19KnNjl_ac9lsWqG-SYpgHAsflK3JXM7h"
	localBasePath := "./downloads"

	log.Printf("Начинаю скачивание папки с ID: %s", folderID)
	log.Printf("Фильтр расширений: %v (пусто = всё)", keys(allowedExtensions))

	if err := downloadFolder(ctx, srv, folderID, localBasePath); err != nil {
		log.Fatalf("Ошибка при скачивании: %v", err)
	}

	log.Println("🎉 Скачивание успешно завершено!")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
