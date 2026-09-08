package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Получить клиента OAuth2, либо из сохранённого токена, либо запросив новый.
func getClient(config *oauth2.Config) *http.Client {
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Перейдите по ссылке для авторизации:\n%v\n", authURL)

	var authCode string
	fmt.Print("Введите код авторизации: ")
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Не удалось прочитать код: %v", err)
	}

	tok, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		log.Fatalf("Не удалось обменять код на токен: %v", err)
	}
	return tok
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Сохраняю токен в %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Не удалось сохранить токен: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func main() {
	ctx := context.Background()

	// Читаем credentials.json
	credsPath := "c:/Users/ahahahah/Desktop/vs/GOProject/Scripts_for_hobby/GoogleDrive_Cleaner/credentials.json"
	b, err := os.ReadFile(credsPath)
	if err != nil {
		log.Fatalf("Не удалось прочитать credentials.json: %v", err)
	}

	// Настраиваем OAuth2 из файла
	config, err := google.ConfigFromJSON(b, drive.DriveScope)
	if err != nil {
		log.Fatalf("Не удалось распарсить credentials.json: %v", err)
	}

	// Получаем HTTP-клиент с авторизацией
	client := getClient(config)

	// Создаём сервис Drive
	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Не удалось создать сервис Drive: %v", err)
	}

	// Поиск и удаление файлов .rpy
	query := "name contains '.rpy' and trashed = false"
	pageToken := ""
	for {
		req := srv.Files.List().Q(query).
			Fields("nextPageToken, files(id, name)").
			PageToken(pageToken)
		res, err := req.Do()
		if err != nil {
			log.Fatalf("Ошибка при поиске файлов: %v", err)
		}

		for _, file := range res.Files {
			fmt.Printf("Удаляю: %s (%s)\n", file.Name, file.Id)
			err := srv.Files.Delete(file.Id).Do()
			if err != nil {
				log.Printf("Ошибка удаления %s: %v", file.Name, err)
			}
		}

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	fmt.Println("Готово!")
}
