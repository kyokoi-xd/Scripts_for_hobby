package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// uniquePath возвращает уникальное имя в целевой папке (добавляет _1, _2...)
func uniquePath(destDir, name string) string {
	ext := filepath.Ext(name)
	nameWithoutExt := strings.TrimSuffix(name, ext)
	counter := 1
	newPath := filepath.Join(destDir, name)
	for {
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			return newPath
		}
		newName := fmt.Sprintf("%s_%d%s", nameWithoutExt, counter, ext)
		newPath = filepath.Join(destDir, newName)
		counter++
	}
}

// copyDir копирует папку рекурсивно
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, os.ModePerm); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile копирует один файл
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Sync()
}

// moveDir перемещает папку (сначала пробует быстрый Rename, при ошибке – копирует и удаляет)
func moveDir(src, dst string, logFunc func(string)) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	logFunc("  → медленное копирование (разные диски)...")
	if err := copyDir(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func main() {
	a := app.New()
	w := a.NewWindow("Перемещение подпапок")
	w.Resize(fyne.NewSize(700, 500))

	// Поля ввода
	srcEntry := widget.NewEntry()
	srcEntry.SetPlaceHolder("Выберите исходную папку")
	dstEntry := widget.NewEntry()
	dstEntry.SetPlaceHolder("Выберите целевую папку")

	// Лог
	logText := widget.NewMultiLineEntry()
	logText.Disable()
	logText.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(logText)
	logScroll.SetMinSize(fyne.NewSize(680, 300))

	// Кнопки "Обзор"
	srcBtn := widget.NewButton("Обзор", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				srcEntry.SetText(uri.Path())
			}
		}, w)
	})
	dstBtn := widget.NewButton("Обзор", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				dstEntry.SetText(uri.Path())
			}
		}, w)
	})

	// Канал для логов (из горутины в главный поток)
	logChan := make(chan string, 100)
	logFunc := func(msg string) {
		logChan <- msg
	}

	// Кнопка "Запустить"
	var runBtn *widget.Button
	runBtn = widget.NewButton("Запустить", func() {
		srcRoot := strings.TrimSpace(srcEntry.Text)
		dstRoot := strings.TrimSpace(dstEntry.Text)
		if srcRoot == "" || dstRoot == "" {
			dialog.ShowInformation("Ошибка", "Укажите обе директории", w)
			return
		}
		srcInfo, err := os.Stat(srcRoot)
		if err != nil || !srcInfo.IsDir() {
			dialog.ShowInformation("Ошибка", "Исходная папка не существует", w)
			return
		}
		if err := os.MkdirAll(dstRoot, os.ModePerm); err != nil {
			dialog.ShowInformation("Ошибка", fmt.Sprintf("Не удалось создать целевую: %v", err), w)
			return
		}

		runBtn.Disable()
		logText.SetText("")
		logChan = make(chan string, 100) // пересоздаём канал

		go func() {
			defer runBtn.Enable()
			items, err := os.ReadDir(srcRoot)
			if err != nil {
				logFunc(fmt.Sprintf("Ошибка чтения исходной директории: %v", err))
				return
			}
			for _, item := range items {
				if !item.IsDir() {
					continue
				}
				subDirPath := filepath.Join(srcRoot, item.Name())
				subItems, err := os.ReadDir(subDirPath)
				if err != nil {
					logFunc(fmt.Sprintf("Не удалось прочитать %s: %v", subDirPath, err))
					continue
				}
				var subDirs []string
				for _, sub := range subItems {
					if sub.IsDir() {
						subDirs = append(subDirs, sub.Name())
					}
				}
				if len(subDirs) == 0 {
					logFunc(fmt.Sprintf("Папка '%s' – нет подпапок", item.Name()))
					continue
				}
				for _, dirName := range subDirs {
					srcPath := filepath.Join(subDirPath, dirName)
					dstPath := uniquePath(dstRoot, dirName)
					logFunc(fmt.Sprintf("Перемещаем '%s' -> '%s'", srcPath, dstPath))
					if err := moveDir(srcPath, dstPath, logFunc); err != nil {
						logFunc(fmt.Sprintf("  Ошибка: %v", err))
					}
				}
				// Удаляем пустую исходную папку
				if err := os.Remove(subDirPath); err == nil {
					logFunc(fmt.Sprintf("Удалена пустая папка '%s'", item.Name()))
				}
			}
			logFunc("✅ Готово!")
		}()
	})

	// Отдельная горутина для обновления лога в UI
	go func() {
		for msg := range logChan {
			fyne.Do(func() {
				logText.SetText(logText.Text + msg + "\n")
				logScroll.ScrollToBottom()
			})
		}
	}()

	// Сборка интерфейса
	form := container.NewGridWithRows(2,
		container.NewBorder(nil, nil, nil, srcBtn, srcEntry),
		container.NewBorder(nil, nil, nil, dstBtn, dstEntry),
	)
	content := container.NewBorder(
		form,
		runBtn,
		nil,
		nil,
		logScroll,
	)
	w.SetContent(container.NewPadded(content))
	w.ShowAndRun()
}
