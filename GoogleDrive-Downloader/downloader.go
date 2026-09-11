package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// downloadFolder рекурсивно обходит папку и скачивает её содержимое.
func downloadFolder(ctx context.Context, srv *drive.Service, folderID, localPath string) error {
	// Получаем список файлов и подпапок внутри текущей папки.
	query := fmt.Sprintf("'%s' in parents and trashed=false", folderID)
	files, err := srv.Files.List().Q(query).Fields("files(id, name, mimeType, size)").Do()
	if err != nil {
		return fmt.Errorf("не удалось получить список файлов для папки %s: %w", folderID, err)
	}

	// Если папка пуста, просто выходим.
	if len(files.Files) == 0 {
		return nil
	}

	// Создаем локальную директорию для текущей папки.
	if err := os.MkdirAll(localPath, os.ModePerm); err != nil {
		return fmt.Errorf("не удалось создать локальную папку %s: %w", localPath, err)
	}

	for _, file := range files.Files {
		filePath := filepath.Join(localPath, file.Name)

		// Если это папка — рекурсивно вызываем downloadFolder.
		if file.MimeType == "application/vnd.google-apps.folder" {
			log.Printf("Вход в папку: %s", filePath)
			if err := downloadFolder(ctx, srv, file.Id, filePath); err != nil {
				return err
			}
		} else {
			// Это файл — скачиваем его.
			log.Printf("Скачивание файла: %s (ID: %s)", filePath, file.Id)
			if err := downloadFile(ctx, srv, file.Id, filePath); err != nil {
				return err
			}
		}
	}
	return nil
}

// downloadFile скачивает один файл, используя возобновляемую загрузку.
func downloadFile(ctx context.Context, srv *drive.Service, fileID, filePath string) error {
	// Создаем локальный файл.
	out, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("не удалось создать локальный файл %s: %w", filePath, err)
	}
	defer out.Close()

	// Вызываем Download для получения файла. Библиотека автоматически использует
	// resumable download для больших файлов.
	resp, err := srv.Files.Get(fileID).Download()
	if err != nil {
		// Обрабатываем возможные ошибки API.
		if gerr, ok := err.(*googleapi.Error); ok {
			return fmt.Errorf("ошибка API при скачивании файла %s (код %d): %w", filePath, gerr.Code, err)
		}
		return fmt.Errorf("не удалось скачать файл %s: %w", filePath, err)
	}
	defer resp.Body.Close()

	// Копируем данные из ответа в локальный файл.
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка при записи файла %s: %w", filePath, err)
	}

	log.Printf("Файл успешно скачан: %s", filePath)
	return nil
}

func main() {
	ctx := context.Background()

	// --- Шаг 1: Аутентификация ---
	// Читаем файл с ключом сервисного аккаунта.
	// Инструкция по созданию ключа: https://console.cloud.google.com/iam-admin/serviceaccounts
	b, err := os.ReadFile("service-account.json")
	if err != nil {
		log.Fatalf("Не удалось прочитать файл ключа service-account.json: %v", err)
	}

	// Создаем конфигурацию с областью доступа только для чтения.
	config, err := google.JWTConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		log.Fatalf("Не удалось создать JWT-конфигурацию: %v", err)
	}

	// Создаем клиент Drive API.
	srv, err := drive.NewService(ctx, option.WithHTTPClient(config.Client(ctx)))
	if err != nil {
		log.Fatalf("Не удалось создать сервис Drive: %v", err)
	}

	// --- Шаг 2: Параметры запуска ---
	// ВАЖНО: Замените эти значения на свои.
	// ID папки можно найти в URL: https://drive.google.com/drive/folders/ЭТОТ_ID
	folderID := "19KnNjl_ac9lsWqG-SYpgHAsflK3JXM7h"
	localBasePath := "./downloads" // Локальная папка для сохранения

	log.Printf("Начинаю скачивание папки с ID: %s", folderID)

	// --- Шаг 3: Запуск рекурсивного скачивания ---
	if err := downloadFolder(ctx, srv, folderID, localBasePath); err != nil {
		log.Fatalf("Ошибка при скачивании: %v", err)
	}

	log.Println("Скачивание успешно завершено!")
}
