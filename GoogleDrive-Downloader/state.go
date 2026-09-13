package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// FileState описывает один файл, который нужно скачать.
type FileState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RelPath  string `json:"rel_path"` // путь относительно localBasePath
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
}

// DownloadState — всё состояние прогона.
type DownloadState struct {
	RootFolderID string      `json:"root_folder_id"`
	LocalBase    string      `json:"local_base"`
	Files        []FileState `json:"files"`
	// Индекс для быстрого доступа (не сериализуется).
	index map[string]int `json:"-"`
	mu    sync.Mutex     `json:"-"`
}

// LoadState читает состояние из файла. Если файла нет — возвращает nil, nil.
func LoadState(path string) (*DownloadState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s DownloadState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("не удалось распарсить %s: %w", path, err)
	}
	s.buildIndex()
	return &s, nil
}

// SaveState атомарно пишет состояние: сначала во временный файл, потом rename.
func (s *DownloadState) SaveState(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *DownloadState) buildIndex() {
	s.index = make(map[string]int, len(s.Files))
	for i, f := range s.Files {
		s.index[f.ID] = i
	}
}

// AddFile добавляет файл, если его ещё нет в состоянии.
func (s *DownloadState) AddFile(f FileState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index == nil {
		s.buildIndex()
	}
	if _, ok := s.index[f.ID]; ok {
		return
	}
	s.index[f.ID] = len(s.Files)
	s.Files = append(s.Files, f)
}
