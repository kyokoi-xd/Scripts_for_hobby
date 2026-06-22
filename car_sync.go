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
	Season    string `json:"season"`    // выбор сезона
}

const (
	appName      = "car_sync"
	settingsFile = "settings.json"
	defaultYear  = 2026
)

func getSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName, settingsFile)
}

func loadSettings() Settings {
	s := Settings{Year: defaultYear}
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
func collectSeasonPaths(root string, year int, season string) ([]string, error) {
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
		if !strings.HasPrefix(name, "S") || len(name) < 2 {
			return nil
		}
		// Проверяем допустимые сезоны
		if season != "" && name != season {
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
func syncDirs(sourceRoot, targetRoot string, year int, season string, overwrite bool, logFunc func(string)) error {
	// Собираем все сезонные папки в целевой
	targetSeasons, err := collectSeasonPaths(targetRoot, year, season)
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

// ---------- Удаление лишних сезонов ----------
// deleteOtherSeasons удаляет все папки сезонов в targetRoot, кроме keepSeason (если указан),
// внутри папки year. Возвращает список удалённых путей для отображения в диалоге.
func deleteOtherSeasons(targetRoot string, year int, keepSeason string, logFunc func(string)) ([]string, error) {
	yearStr := strconv.Itoa(year)
	var toDelete []string

	// Сначала соберём все папки сезонов, которые нужно удалить
	err := filepath.WalkDir(targetRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "S") || len(name) < 2 {
			return nil
		}
		// Пропускаем, если это сохраняемый сезон
		if keepSeason != "" && name == keepSeason {
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		if parent != yearStr {
			return nil
		}
		rel, err := filepath.Rel(targetRoot, path)
		if err != nil {
			return err
		}
		toDelete = append(toDelete, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(toDelete) == 0 {
		logFunc("Нет папок для удаления.")
		return toDelete, nil
	}

	// Удаляем
	for _, rel := range toDelete {
		fullPath := filepath.Join(targetRoot, rel)
		logFunc("Удаляем: " + rel)
		if err := os.RemoveAll(fullPath); err != nil {
			logFunc("Ошибка удаления " + rel + ": " + err.Error())
		} else {
			logFunc("Успешно удалено: " + rel)
		}
	}
	return toDelete, nil
}

// ---------- GUI ----------
func main() {
	a := app.New()
	w := a.NewWindow("Синхронизация Wxx")
	w.Resize(fyne.NewSize(1000, 750))

	settings := loadSettings()

	// Поля ввода (расширенные)
	sourceEntry := widget.NewEntry()
	sourceEntry.SetText(settings.SourceDir)
	sourceEntry.Resize(fyne.NewSize(400, 1110))

	targetEntry := widget.NewEntry()
	targetEntry.SetText(settings.TargetDir)
	targetEntry.Resize(fyne.NewSize(400, 1110))

	yearEntry := widget.NewEntry()
	yearEntry.SetText(strconv.Itoa(settings.Year))
	yearEntry.Resize(fyne.NewSize(100, 1110))

	seasonEntry := widget.NewEntry()
	seasonEntry.SetText(settings.Season)
	seasonEntry.Resize(fyne.NewSize(80, 1110))
	seasonEntry.SetPlaceHolder("S3")

	overwriteCheck := widget.NewCheck("Перезаписывать существующие папки Wxx (удалить и скопировать заново)", func(b bool) {})
	overwriteCheck.SetChecked(settings.Overwrite)

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

	// Кнопка запуска синхронизации
	runBtn := widget.NewButton("Запустить синхронизацию", func() {
		settings.SourceDir = sourceEntry.Text
		settings.TargetDir = targetEntry.Text
		settings.Overwrite = overwriteCheck.Checked
		year, _ := strconv.Atoi(yearEntry.Text)
		if year > 2000 {
			settings.Year = year
		}
		settings.Season = strings.TrimSpace(seasonEntry.Text)
		saveSettings(settings)

		logText.SetText("")
		logFunc := func(msg string) {
			logText.SetText(logText.Text + msg + "\n")
		}

		go func() {
			logFunc("Начинаем синхронизацию...")
			err := syncDirs(settings.SourceDir, settings.TargetDir, settings.Year, settings.Season, settings.Overwrite, logFunc)
			if err != nil {
				logFunc("Ошибка: " + err.Error())
			} else {
				logFunc("Синхронизация завершена.")
			}
		}()
	})

	// Кнопка удаления лишних сезонов
	deleteBtn := widget.NewButton("Удалить лишние сезоны (кроме выбранного)", func() {
		// Сохраняем текущие настройки
		settings.SourceDir = sourceEntry.Text
		settings.TargetDir = targetEntry.Text
		settings.Overwrite = overwriteCheck.Checked
		year, _ := strconv.Atoi(yearEntry.Text)
		if year > 2000 {
			settings.Year = year
		}
		settings.Season = strings.TrimSpace(seasonEntry.Text)
		saveSettings(settings)

		if settings.TargetDir == "" {
			dialog.ShowInformation("Ошибка", "Целевая директория не выбрана.", w)
			return
		}

		// Сначала соберём список того, что будет удалено (для отображения)
		toDelete, err := deleteOtherSeasons(settings.TargetDir, settings.Year, settings.Season, func(msg string) {})
		if err != nil {
			dialog.ShowInformation("Ошибка", "Не удалось собрать список: "+err.Error(), w)
			return
		}
		if len(toDelete) == 0 {
			dialog.ShowInformation("Информация", "Нет папок для удаления.", w)
			return
		}

		// Формируем сообщение подтверждения
		msg := "Будут удалены следующие папки сезонов (кроме '" + settings.Season + "'):\n\n"
		for _, rel := range toDelete {
			msg += "  • " + rel + "\n"
		}
		msg += "\nПродолжить?"

		dialog.ShowConfirm("Подтверждение удаления", msg, func(confirm bool) {
			if !confirm {
				return
			}
			// Выполняем удаление с логированием
			logText.SetText("")
			logFunc := func(msg string) {
				logText.SetText(logText.Text + msg + "\n")
			}
			go func() {
				logFunc("Начинаем удаление лишних сезонов...")
				_, err := deleteOtherSeasons(settings.TargetDir, settings.Year, settings.Season, logFunc)
				if err != nil {
					logFunc("Ошибка: " + err.Error())
				} else {
					logFunc("Удаление завершено.")
				}
			}()
		}, w)
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
			widget.NewLabel("Сезон (например, S3):"),
			seasonEntry,
			overwriteCheck,
		),

		container.NewHBox(
			runBtn,
			deleteBtn,
		),

		widget.NewLabel("Лог операций:"),
		logText,
	)

	w.SetContent(form)
	w.ShowAndRun()
}
