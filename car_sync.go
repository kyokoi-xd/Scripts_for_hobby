package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ---------- Настройки ----------
type Settings struct {
	SourceDir string `json:"source_dir"`
	TargetDir string `json:"target_dir"`
	Overwrite bool   `json:"overwrite"` // перезаписывать существующие папки Wxx
	Year      int    `json:"year"`      // год (по умолчанию 2026)
}

const (
	appName        = "wxxsync"
	settingsFile   = "settings.json"
	defaultYear    = 2026
	allowedSeasons = "S2,S3,S4" // можно расширить
)

func getSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName, settingsFile)
}

func loadSettings() Settings {
	var s Settings
	s.Year = defaultYear
	path := getSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

func saveSettings(s Settings) {
	path := getSettingsPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(path, data, 0644)
}

// ---------- Утилиты для копирования ----------
func copyFile(src, dst string) error {
	srcF, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcF.Close()
	dstF, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstF.Close()
	_, err = io.Copy(dstF, srcF)
	return err
}

// copyDir рекурсивно копирует папку src в dst (dst будет создана)
func copyDir(src, dst string) error {
	// Создаём целевую папку
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
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

// ---------- Основная логика синхронизации ----------
// collectSeasonPaths собирает все пути к папкам сезонов (S2/S3/S4) внутри year
// относительно корня root, но только если родительская папка = year.
func collectSeasonPaths(root string, year int) ([]string, error) {
	var seasons []string
	yearStr := strconv.Itoa(year)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		// Проверяем, что имя папки - сезон (S2, S3, S4)
		if !strings.HasPrefix(name, "S") || len(name) != 2 {
			return nil
		}
		// Проверяем допустимые сезоны
		if name != "S2" && name != "S3" && name != "S4" {
			return nil
		}
		// Проверяем, что родительская папка называется yearStr
		parent := filepath.Base(filepath.Dir(path))
		if parent != yearStr {
			return nil
		}
		// Получаем относительный путь
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		seasons = append(seasons, rel)
		return nil
	})
	return seasons, err
}

// syncDirs выполняет синхронизацию
func syncDirs(sourceRoot, targetRoot string, year int, overwrite bool, logFunc func(string)) error {
	// Собираем все сезонные папки в целевой
	targetSeasons, err := collectSeasonPaths(targetRoot, year)
	if err != nil {
		return err
	}
	logFunc("Найдено сезонных папок в целевой: " + string(rune(len(targetSeasons))))

	for _, relSeason := range targetSeasons {
		sourceSeason := filepath.Join(sourceRoot, relSeason)
		// Проверяем, есть ли такой сезон в исходной
		if _, err := os.Stat(sourceSeason); os.IsNotExist(err) {
			logFunc("Пропускаем (нет в исходной): " + relSeason)
			continue
		}
		// Читаем папки Wxx в исходной
		entries, err := os.ReadDir(sourceSeason)
		if err != nil {
			logFunc("Ошибка чтения " + sourceSeason + ": " + err.Error())
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wName := e.Name()
			if !strings.HasPrefix(wName, "W") || len(wName) < 3 {
				continue
			}
			// Проверяем, что это действительно Wxx (можно уточнить)
			// Формируем пути
			srcW := filepath.Join(sourceSeason, wName)
			dstW := filepath.Join(targetRoot, relSeason, wName)

			// Проверяем существование в целевой
			if _, err := os.Stat(dstW); err == nil {
				if !overwrite {
					logFunc("Пропускаем (уже есть): " + filepath.Join(relSeason, wName))
					continue
				} else {
					// Удаляем старую папку
					if err := os.RemoveAll(dstW); err != nil {
						logFunc("Ошибка удаления " + dstW + ": " + err.Error())
						continue
					}
					logFunc("Удалена старая: " + filepath.Join(relSeason, wName))
				}
			}
			// Копируем папку Wxx
			logFunc("Копируем: " + filepath.Join(relSeason, wName))
			if err := copyDir(srcW, dstW); err != nil {
				logFunc("Ошибка копирования: " + err.Error())
			} else {
				logFunc("Успешно скопировано: " + filepath.Join(relSeason, wName))
			}
		}
	}
	return nil
}

// ---------- GUI ----------
func main() {
	a := app.New()
	w := a.NewWindow("Синхронизация Wxx")
	w.Resize(fyne.NewSize(900, 700))

	// Загружаем настройки
	settings := loadSettings()

	// Элементы интерфейса
	sourceEntry := widget.NewEntry()
	sourceEntry.SetText(settings.SourceDir)
	targetEntry := widget.NewEntry()
	targetEntry.SetText(settings.TargetDir)

	overwriteCheck := widget.NewCheck("Перезаписывать существующие папки Wxx (удалить и скопировать заново)", func(b bool) {})
	overwriteCheck.SetChecked(settings.Overwrite)

	yearEntry := widget.NewEntry()
	yearEntry.SetText(string(rune('0'+settings.Year/1000)) + string(rune('0'+(settings.Year/100)%10)) +
		string(rune('0'+(settings.Year/10)%10)) + string(rune('0'+settings.Year%10)))
	// Упростим через fmt, но для краткости оставлю так (лучше использовать strconv.Itoa)

	logText := widget.NewMultiLineEntry()
	logText.SetMinRowsVisible(15)
	logText.Disable()

	// Кнопки выбора папок
	chooseSource := widget.NewButton("Выбрать исходную (полную)", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				sourceEntry.SetText(uri.Path())
			}
		}, w)
	})
	chooseTarget := widget.NewButton("Выбрать целевую (неполную)", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				targetEntry.SetText(uri.Path())
			}
		}, w)
	})

	// Кнопка запуска
	runBtn := widget.NewButton("Запустить синхронизацию", func() {
		// Сохраняем настройки
		settings.SourceDir = sourceEntry.Text
		settings.TargetDir = targetEntry.Text
		settings.Overwrite = overwriteCheck.Checked
		// Парсим год
		year := defaultYear
		if y, err := strconv.Atoi(yearEntry.Text); err == nil && y > 2000 {
			year = y
		}
		settings.Year = year
		saveSettings(settings)

		// Очищаем лог
		logText.SetText("")
		logFunc := func(msg string) {
			logText.SetText(logText.Text + msg + "\n")
		}

		// Запускаем синхронизацию в горутине, чтобы не блокировать UI
		go func() {
			logFunc("Начинаем синхронизацию...")
			err := syncDirs(settings.SourceDir, settings.TargetDir, settings.Year, settings.Overwrite, logFunc)
			if err != nil {
				logFunc("Ошибка: " + err.Error())
			} else {
				logFunc("Синхронизация завершена.")
			}
		}()
	})

	// Компоновка
	form := container.NewVBox(
		widget.NewLabel("Исходная директория (полная, содержит все Wxx):"),
		container.NewHBox(sourceEntry, chooseSource),
		widget.NewLabel("Целевая директория (неполная, куда копировать):"),
		container.NewHBox(targetEntry, chooseTarget),
		container.NewHBox(
			widget.NewLabel("Год (папка):"),
			yearEntry,
			overwriteCheck,
		),
		runBtn,
		widget.NewLabel("Лог операций:"),
		logText,
	)

	w.SetContent(form)
	w.ShowAndRun()
}
