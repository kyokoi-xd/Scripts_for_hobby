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

// Папки с этими именами (точное совпадение) не обходятся и не скачиваются.
// Регистр не учитывается.
var skipFolders = map[string]bool{
	"s1": true,
	"s2": true,
	"s3": true,
	// "temp": true,
	// "old":  true,
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

// collectFolder рекурсивно обходит дерево и наполняет состояние.
// Возвращает счётчик добавленных новых файлов.
func collectFolder(ctx context.Context, srv *drive.Service, folderID, relPath string, st *DownloadState) (int, error) {
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

		var res *drive.FileList
		// Оборачиваем вызов API в retry
		err := withRetry(ctx, "list folder "+folderID, func() error {
			r, e := call.Do()
			if e != nil {
				return e
			}
			res = r
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("list folder %s: %w", folderID, err)
		}

		allFiles = append(allFiles, res.Files...)
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}

	added := 0
	for _, file := range allFiles {
		childRel := filepath.Join(relPath, file.Name)

		if file.MimeType == "application/vnd.google-apps.folder" {
			// ⬇️ НОВОЕ: пропускаем ненужные папки
			if skipFolders[strings.ToLower(file.Name)] {
				log.Printf("⏭️  Пропуск папки: %s", childRel)
				continue
			}

			log.Printf("📁 Обход: %s", childRel)
			n, err := collectFolder(ctx, srv, file.Id, childRel, st)
			if err != nil {
				return added, err
			}
			added += n
			continue
		}

		if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.") {
			continue
		}
		if !shouldDownload(file.Name) {
			continue
		}

		// Добавляем только если такого ID ещё нет в состоянии.
		if !st.HasFile(file.Id) {
			st.AddFile(FileState{
				ID:       file.Id,
				Name:     file.Name,
				RelPath:  childRel,
				Size:     file.Size,
				MimeType: file.MimeType,
			})
			added++
		}
	}
	return added, nil
}

// downloadAll проходит по состоянию и качает то, что ещё не скачано.
func downloadAll(ctx context.Context, srv *drive.Service, st *DownloadState) error {
	total := len(st.Files)
	log.Printf("📋 В очереди файлов: %d", total)

	for i, f := range st.Files {
		filePath := filepath.Join(st.LocalBase, f.RelPath)

		// Ранний выход: если файл уже на диске и размер совпадает — пропускаем.
		if info, err := os.Stat(filePath); err == nil && f.Size > 0 && info.Size() == f.Size {
			log.Printf("[%d/%d] ✅ Уже есть: %s", i+1, total, f.RelPath)
			continue
		}

		// Гарантируем, что родительская директория существует.
		if err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(filePath), err)
		}

		log.Printf("[%d/%d] ⬇️  %s", i+1, total, f.RelPath)
		if err := downloadFileWithRetry(ctx, srv, f.ID, filePath, f.Size); err != nil {
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
		log.Fatalf("JWT: %v", err)
	}
	srv, err := drive.NewService(ctx, option.WithHTTPClient(config.Client(ctx)))
	if err != nil {
		log.Fatalf("Drive service: %v", err)
	}

	folderID := "19KnNjl_ac9lsWqG-SYpgHAsflK3JXM7h"
	localBasePath := "./downloads"
	statePath := ".download_state.json"

	// --- Загружаем состояние (или создаём новое) ---
	st, err := LoadState(statePath)
	if err != nil {
		log.Fatalf("Не удалось загрузить состояние: %v", err)
	}

	if st == nil || st.RootFolderID != folderID {
		log.Printf("🔍 Состояние не найдено или от другой папки — создаю новое")
		st = &DownloadState{
			RootFolderID: folderID,
			LocalBase:    localBasePath,
		}
	} else {
		log.Printf("♻️  Загружено состояние: %d файлов", len(st.Files))
	}

	// --- ВСЕГДА обходим дерево, добавляя только новые файлы ---
	log.Printf("🔍 Сканирую дерево на новые файлы...")
	added, err := collectFolder(ctx, srv, folderID, "", st)
	if err != nil {
		// Сохраняем то, что успели собрать — не теряем прогресс
		if saveErr := st.SaveState(statePath); saveErr != nil {
			log.Printf("⚠️  Не удалось сохранить частичное состояние: %v", saveErr)
		}
		log.Fatalf("Ошибка при обходе: %v", err)
	}

	if added > 0 {
		log.Printf("🆕 Добавлено новых файлов: %d", added)
	} else {
		log.Printf("📭 Новых файлов не найдено")
	}
	log.Printf("📋 Всего файлов в очереди: %d", len(st.Files))

	if err := st.SaveState(statePath); err != nil {
		log.Fatalf("Не удалось сохранить состояние: %v", err)
	}

	// --- Скачиваем всё, что не скачано ---
	if err := downloadAll(ctx, srv, st); err != nil {
		log.Fatalf("Ошибка при скачивании: %v", err)
	}

	log.Println("🎉 Всё скачано!")
}
