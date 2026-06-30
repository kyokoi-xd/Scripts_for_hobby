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
	SourceDir  string   `json:"source_dir"`
	TargetDir  string   `json:"target_dir"`
	Overwrite  bool     `json:"overwrite"`  // перезаписывать существующие папки Wxx
	Year       int      `json:"year"`       // год (по умолчанию 2026)
	Season     string   `json:"season"`     // выбор сезона
	Extensions []string `json:"extensions"` // расширение файлов (например, .sto)
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

// copyDir копирует содержимое src в dst рекурсивно.
func copyDir(src, dst string) error {
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

// ---------- Синхронизация с фильтром расширений и прогрессом ----------
func syncDirs(sourceRoot, targetRoot string, year int, season string, overwrite bool, extensions []string,
	logFunc func(string), progressCallback func(float64)) error {
	// Собираем все сезонные папки в целевой
	targetSeasons, err := collectSeasonPaths(targetRoot, year, season)
	if err != nil {
		return err
	}
	logFunc("Найдено сезонных папок в целевой: " + strconv.Itoa(len(targetSeasons)))
	// Если extensions пуст, копируем все файлы
	if len(extensions) == 0 {
		var totalWxx int
		for _, relSeason := range targetSeasons {
			sourceSeason := filepath.Join(sourceRoot, relSeason)
			if _, err := os.Stat(sourceSeason); os.IsNotExist(err) {
				logFunc("Пропускаем (нет в исходной): " + relSeason)
				continue
			}
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
				srcW := filepath.Join(sourceSeason, wName)
				dstW := filepath.Join(targetRoot, relSeason, wName)
				// Проверяем существование целевой папки
				if _, err := os.Stat(dstW); err == nil {
					if !overwrite {
						logFunc("Пропускаем (уже есть): " + filepath.Join(relSeason, wName))
						continue
					} else {
						if err := os.RemoveAll(dstW); err != nil {
							logFunc("Ошибка удаления " + dstW + ": " + err.Error())
							continue
						}
						logFunc("Удалена старая: " + filepath.Join(relSeason, wName))
					}
				}
				logFunc("Копируем папку: " + filepath.Join(relSeason, wName))
				if err := copyDir(srcW, dstW); err != nil {
					logFunc("Ошибка копирования папки " + err.Error())
				} else {
					logFunc("Успешно скопировано: " + filepath.Join(relSeason, wName))
				}
				totalWxx++
				progressCallback(float64(totalWxx) / float64(len(targetSeasons)))
			}
		}
		logFunc("Синхронизация завершена. Всего скопировано папок Wxx: " + strconv.Itoa(totalWxx))
		return nil
	}
	// Режим с фильтром расширений (рекурсивный обход внутри Wxx)
	type fileJob struct {
		src string
		dst string
	}
	var jobs []fileJob
	var wxxProcessed int

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

			// Проверяем существование целевой папки
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

			if err := os.MkdirAll(dstW, 0755); err != nil {
				logFunc("Ошибка создания папки " + dstW + ": " + err.Error())
				continue
			}
			// Рекурсивно собираем файлы с нужными расширениями
			var fileCount int
			err = filepath.WalkDir(srcW, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(d.Name()))
				// Проверяем, что расширение в списке
				found := false
				for _, e := range extensions {
					if ext == e {
						found = true
						break
					}
				}
				if !found {
					return nil
				}
				// Вычисляем относительный путь файла относительно srcW
				rel, err := filepath.Rel(srcW, path)
				if err != nil {
					return err
				}
				dstFile := filepath.Join(dstW, rel)
				jobs = append(jobs, fileJob{src: path, dst: dstFile})
				fileCount++
				return nil
			})
			if err != nil {
				logFunc("Ошибка обхода папки " + srcW + ": " + err.Error())
				continue
			}
			if fileCount > 0 {
				logFunc("Найдено " + strconv.Itoa(fileCount) + " файлов в " + filepath.Join(relSeason, wName))
				wxxProcessed++
			} else {
				logFunc("Нет поддерживаемых файлов в " + filepath.Join(relSeason, wName) + " (папка создана)")
			}
		}
	}

	if len(jobs) == 0 {
		logFunc("Нет файлов для копирования (с выбранными расширениями).")
		progressCallback(1.0)
		return nil
	}

	logFunc("Всего файлов для копирования: " + strconv.Itoa(len(jobs)))

	// Копируем файлы
	processed := 0
	for _, job := range jobs {
		// Создаём директорию назначения, если её нет
		if err := os.MkdirAll(filepath.Dir(job.dst), 0755); err != nil {
			logFunc("Ошибка создания папки " + filepath.Dir(job.dst) + ": " + err.Error())
			continue
		}
		if err := copyFile(job.src, job.dst); err != nil {
			logFunc("Ошибка копирования " + job.src + " -> " + job.dst + ": " + err.Error())
		} else {
			logFunc("Успешно скопировано " + filepath.Base(job.dst))
		}
		processed++
		progressCallback(float64(processed) / float64(len(jobs)))
	}
	logFunc("Синхронизация завершена. Скопировано файлов: " + strconv.Itoa(processed))
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
	w.Resize(fyne.NewSize(1100, 800))

	settings := loadSettings()

	// Поля ввода (расширенные)
	sourceEntry := widget.NewEntry()
	sourceEntry.SetText(settings.SourceDir)
	sourceEntry.Resize(fyne.NewSize(400, 0))

	targetEntry := widget.NewEntry()
	targetEntry.SetText(settings.TargetDir)
	targetEntry.Resize(fyne.NewSize(400, 0))

	yearEntry := widget.NewEntry()
	yearEntry.SetText(strconv.Itoa(settings.Year))
	yearEntry.Resize(fyne.NewSize(100, 0))

	seasonEntry := widget.NewEntry()
	seasonEntry.SetText(settings.Season)
	seasonEntry.Resize(fyne.NewSize(80, 0))
	seasonEntry.SetPlaceHolder("S3")

	overwriteCheck := widget.NewCheck("Перезаписывать существующие папки Wxx (удалить и скопировать заново)", func(b bool) {})
	overwriteCheck.SetChecked(settings.Overwrite)

	// Чекбоксы для расширений
	extensions := []string{".sto", ".rpy", ".blap", ".olap", ".ibt"}
	extChecks := make(map[string]*widget.Check)
	for _, ext := range extensions {
		check := widget.NewCheck(ext, func(bool) {})
		for _, savedExt := range settings.Extensions {
			if savedExt == ext {
				check.SetChecked(true)
				break
			}
		}
		extChecks[ext] = check
	}

	// Progress bar
	progressBar := widget.NewProgressBar()
	progressBar.Min = 0
	progressBar.Max = 1.0
	progressBar.SetValue(0)

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
		// Собираем выбранные расширения
		var selectedExts []string
		for ext, check := range extChecks {
			if check.Checked {
				selectedExts = append(selectedExts, ext)
			}
		}
		// Сохраняем текущие настройки
		settings.SourceDir = sourceEntry.Text
		settings.TargetDir = targetEntry.Text
		settings.Overwrite = overwriteCheck.Checked
		year, _ := strconv.Atoi(yearEntry.Text)
		if year > 2000 {
			settings.Year = year
		}
		settings.Season = strings.TrimSpace(seasonEntry.Text)
		settings.Extensions = selectedExts
		saveSettings(settings)

		fyne.Do(func() {
			logText.SetText("")
			progressBar.SetValue(0)
		})

		logFunc := func(msg string) {
			fyne.Do(func() {
				logText.SetText(logText.Text + msg + "\n")
			})
		}
		progressFunc := func(percent float64) {
			fyne.Do(func() {
				progressBar.SetValue(percent)
			})
		}

		go func() {
			logFunc("Начинаем синхронизацию...")
			err := syncDirs(settings.SourceDir, settings.TargetDir, settings.Year, settings.Season,
				settings.Overwrite, settings.Extensions, logFunc, progressFunc)
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
		// Разбираем выбранные расширения (для сохранения, хотя они не нужны для удаления)
		var selectedExts []string
		for ext, check := range extChecks {
			if check.Checked {
				selectedExts = append(selectedExts, ext)
			}
		}
		settings.Extensions = selectedExts
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
			fyne.Do(func() {
				logText.SetText("")
				progressBar.SetValue(0)
			})

			logFunc := func(msg string) {
				fyne.Do(func() {
					logText.SetText(logText.Text + msg + "\n")
				})
			}
			go func() {
				logFunc("Начинаем удаление лишних сезонов...")
				_, err := deleteOtherSeasons(settings.TargetDir, settings.Year, settings.Season, logFunc)
				if err != nil {
					logFunc("Ошибка: " + err.Error())
				} else {
					logFunc("Удаление завершено.")
				}
				fyne.Do(func() {
					progressBar.SetValue(1.0)
				})
			}()
		}, w)
	})
	// Компоновка чекбоксов расширений
	extContainer := container.NewHBox()
	for _, ext := range extensions {
		extContainer.Add(extChecks[ext])
	}

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
		),
		container.NewHBox(overwriteCheck),
		widget.NewLabel("Расширения файлов для копирования (если ничего не выбрано - копируются все):"),
		extContainer,

		container.NewHBox(runBtn, deleteBtn),

		widget.NewLabel("Прогресс:"),
		progressBar,
		widget.NewLabel("Лог операций:"),
		logText,
	)

	w.SetContent(form)
	w.ShowAndRun()
}
