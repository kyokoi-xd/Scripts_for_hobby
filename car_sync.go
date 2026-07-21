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
	Overwrite  bool     `json:"overwrite"`
	Year       int      `json:"year"`
	Season     string   `json:"season"`
	Extensions []string `json:"extensions"`
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

// syncFolder копирует недостающие элементы из src в dst (инкрементально).
// Логирует только копирование целых папок и итоговое количество скопированных элементов.
func syncFolder(src, dst string, logFunc func(string)) (copiedCount int, err error) {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if _, err := os.Stat(dstPath); os.IsNotExist(err) {
				logFunc("  Копируем папку: " + e.Name())
				if err := copyDir(srcPath, dstPath); err != nil {
					return copiedCount, err
				}
				copiedCount++
			} else {
				subCopied, err := syncFolder(srcPath, dstPath, logFunc)
				copiedCount += subCopied
				if err != nil {
					return copiedCount, err
				}
			}
		} else {
			if _, err := os.Stat(dstPath); os.IsNotExist(err) {
				if err := copyFile(srcPath, dstPath); err != nil {
					return copiedCount, err
				}
				copiedCount++
			}
		}
	}
	return copiedCount, nil
}

// ---------- Основная логика синхронизации ----------
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
		if !strings.HasPrefix(name, "S") || len(name) < 2 {
			return nil
		}
		if season != "" && name != season {
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		if parent != yearStr {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		seasons = append(seasons, rel)
		return nil
	})
	return seasons, err
}

func syncDirs(sourceRoot, targetRoot string, year int, season string, overwrite bool, extensions []string,
	logFunc func(string), progressCallback func(float64)) error {

	targetSeasons, err := collectSeasonPaths(targetRoot, year, season)
	if err != nil {
		return err
	}
	logFunc("Найдено сезонных папок: " + strconv.Itoa(len(targetSeasons)))

	// ---- Режим без фильтра расширений ----
	if len(extensions) == 0 {
		var totalFolders int
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
				folderName := e.Name()
				isWxx := strings.HasPrefix(folderName, "W") && len(folderName) >= 3
				isNEC := folderName == "NEC"
				if !isWxx && !isNEC {
					continue
				}
				srcFolder := filepath.Join(sourceSeason, folderName)
				dstFolder := filepath.Join(targetRoot, relSeason, folderName)

				if _, err := os.Stat(dstFolder); err == nil {
					if !overwrite {
						logFunc("Обновляем (дозаполнение): " + filepath.Join(relSeason, folderName))
						copied, err := syncFolder(srcFolder, dstFolder, logFunc)
						if err != nil {
							logFunc("Ошибка синхронизации: " + err.Error())
						} else {
							logFunc("Добавлено элементов: " + strconv.Itoa(copied))
						}
						totalFolders++
						progressCallback(float64(totalFolders) / float64(len(targetSeasons)))
						continue
					} else {
						if err := os.RemoveAll(dstFolder); err != nil {
							logFunc("Ошибка удаления " + dstFolder + ": " + err.Error())
							continue
						}
						logFunc("Удалена старая: " + filepath.Join(relSeason, folderName))
					}
				}
				logFunc("Копируем папку: " + filepath.Join(relSeason, folderName))
				if err := copyDir(srcFolder, dstFolder); err != nil {
					logFunc("Ошибка копирования: " + err.Error())
				} else {
					logFunc("Успешно скопировано.")
				}
				totalFolders++
				progressCallback(float64(totalFolders) / float64(len(targetSeasons)))
			}
		}
		logFunc("Синхронизация завершена. Обработано папок: " + strconv.Itoa(totalFolders))
		return nil
	}

	// ---- Режим с фильтром расширений ----
	type fileJob struct {
		src string
		dst string
	}
	var jobs []fileJob
	var processedFolders int

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
			folderName := e.Name()
			isWxx := strings.HasPrefix(folderName, "W") && len(folderName) >= 3
			isNEC := folderName == "NEC"
			if !isWxx && !isNEC {
				continue
			}
			srcFolder := filepath.Join(sourceSeason, folderName)
			dstFolder := filepath.Join(targetRoot, relSeason, folderName)

			// Если папка существует и перезапись выключена – дозаполнение
			if _, err := os.Stat(dstFolder); err == nil && !overwrite {
				logFunc("Обновляем (дозаполнение) " + filepath.Join(relSeason, folderName))
				var localJobs []fileJob
				var fileCount int
				err = filepath.WalkDir(srcFolder, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					ext := strings.ToLower(filepath.Ext(d.Name()))
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
					rel, err := filepath.Rel(srcFolder, path)
					if err != nil {
						return err
					}
					dstFile := filepath.Join(dstFolder, rel)
					if _, err := os.Stat(dstFile); os.IsNotExist(err) {
						localJobs = append(localJobs, fileJob{src: path, dst: dstFile})
						fileCount++
					}
					return nil
				})
				if err != nil {
					logFunc("Ошибка обхода папки: " + err.Error())
					continue
				}
				if fileCount > 0 {
					logFunc("Найдено новых файлов: " + strconv.Itoa(fileCount))
					jobs = append(jobs, localJobs...)
				} else {
					logFunc("Новых файлов нет.")
				}
				processedFolders++
				continue
			}

			// Иначе – удаляем, если включена перезапись, и копируем все подходящие файлы
			if _, err := os.Stat(dstFolder); err == nil && overwrite {
				if err := os.RemoveAll(dstFolder); err != nil {
					logFunc("Ошибка удаления " + dstFolder + ": " + err.Error())
					continue
				}
				logFunc("Удалена старая: " + filepath.Join(relSeason, folderName))
			}
			if err := os.MkdirAll(dstFolder, 0755); err != nil {
				logFunc("Ошибка создания папки: " + err.Error())
				continue
			}
			var fileCount int
			err = filepath.WalkDir(srcFolder, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(d.Name()))
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
				rel, err := filepath.Rel(srcFolder, path)
				if err != nil {
					return err
				}
				dstFile := filepath.Join(dstFolder, rel)
				jobs = append(jobs, fileJob{src: path, dst: dstFile})
				fileCount++
				return nil
			})
			if err != nil {
				logFunc("Ошибка обхода: " + err.Error())
				continue
			}
			if fileCount > 0 {
				logFunc("Найдено файлов для копирования: " + strconv.Itoa(fileCount))
				processedFolders++
			} else {
				logFunc("Нет файлов с выбранными расширениями.")
			}
		}
	}

	if len(jobs) == 0 {
		logFunc("Нет файлов для копирования.")
		progressCallback(1.0)
		return nil
	}

	logFunc("Всего файлов для копирования: " + strconv.Itoa(len(jobs)))

	processed := 0
	for _, job := range jobs {
		if err := os.MkdirAll(filepath.Dir(job.dst), 0755); err != nil {
			logFunc("Ошибка создания папки: " + err.Error())
			continue
		}
		if err := copyFile(job.src, job.dst); err != nil {
			logFunc("Ошибка копирования " + filepath.Base(job.src) + ": " + err.Error())
		}
		processed++
		if processed%10 == 0 || processed == len(jobs) {
			progressCallback(float64(processed) / float64(len(jobs)))
		}
	}
	progressCallback(1.0)
	logFunc("Синхронизация завершена. Скопировано файлов: " + strconv.Itoa(processed))
	return nil
}

// ---------- Удаление лишних сезонов (без изменений) ----------
func deleteOtherSeasons(targetRoot string, year int, keepSeason string, logFunc func(string)) ([]string, error) {
	yearStr := strconv.Itoa(year)
	var toDelete []string
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
	for _, rel := range toDelete {
		fullPath := filepath.Join(targetRoot, rel)
		logFunc("Удаляем: " + rel)
		if err := os.RemoveAll(fullPath); err != nil {
			logFunc("Ошибка удаления: " + err.Error())
		} else {
			logFunc("Удалено.")
		}
	}
	return toDelete, nil
}

// ---------- GUI (без изменений) ----------
func main() {
	a := app.NewWithID(appName)
	w := a.NewWindow("Синхронизация Wxx")

	settings := loadSettings()

	var (
		sourceEntry    *widget.Entry
		targetEntry    *widget.Entry
		yearEntry      *widget.Entry
		seasonEntry    *widget.Entry
		progressBar    *widget.ProgressBar
		logText        *widget.Entry
		overwriteCheck *widget.Check
		extChecks      map[string]*widget.Check
		chooseSource   *widget.Button
		chooseTarget   *widget.Button
		runBtn         *widget.Button
		deleteBtn      *widget.Button
	)

	w.Resize(fyne.NewSize(1100, 800))
	w.CenterOnScreen()

	sourceEntry = widget.NewEntry()
	sourceEntry.SetText(settings.SourceDir)
	sourceEntry.Resize(fyne.NewSize(400, 0))

	targetEntry = widget.NewEntry()
	targetEntry.SetText(settings.TargetDir)
	targetEntry.Resize(fyne.NewSize(400, 0))

	yearEntry = widget.NewEntry()
	yearEntry.SetText(strconv.Itoa(settings.Year))
	yearEntry.Resize(fyne.NewSize(100, 0))

	seasonEntry = widget.NewEntry()
	seasonEntry.SetText(settings.Season)
	seasonEntry.Resize(fyne.NewSize(80, 0))
	seasonEntry.SetPlaceHolder("S3")

	overwriteCheck = widget.NewCheck("Перезаписывать существующие папки Wxx (удалить и скопировать заново)", func(b bool) {})
	overwriteCheck.SetChecked(settings.Overwrite)

	extensions := []string{".sto", ".rpy", ".blap", ".olap", ".ibt"}
	extChecks = make(map[string]*widget.Check)
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

	progressBar = widget.NewProgressBar()
	progressBar.Min = 0
	progressBar.Max = 1.0
	progressBar.SetValue(0)

	logText = widget.NewMultiLineEntry()
	logText.SetMinRowsVisible(15)
	logText.Disable()

	chooseSource = widget.NewButton("Выбрать исходную (полную)", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				sourceEntry.SetText(uri.Path())
			}
		}, w)
	})
	chooseTarget = widget.NewButton("Выбрать целевую (неполную)", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				targetEntry.SetText(uri.Path())
			}
		}, w)
	})

	runBtn = widget.NewButton("Запустить синхронизацию", func() {
		var selectedExts []string
		for ext, check := range extChecks {
			if check.Checked {
				selectedExts = append(selectedExts, ext)
			}
		}
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

	deleteBtn = widget.NewButton("Удалить лишние сезоны (кроме выбранного)", func() {
		settings.SourceDir = sourceEntry.Text
		settings.TargetDir = targetEntry.Text
		settings.Overwrite = overwriteCheck.Checked
		year, _ := strconv.Atoi(yearEntry.Text)
		if year > 2000 {
			settings.Year = year
		}
		settings.Season = strings.TrimSpace(seasonEntry.Text)
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

		toDelete, err := deleteOtherSeasons(settings.TargetDir, settings.Year, settings.Season, func(msg string) {})
		if err != nil {
			dialog.ShowInformation("Ошибка", "Не удалось собрать список: "+err.Error(), w)
			return
		}
		if len(toDelete) == 0 {
			dialog.ShowInformation("Информация", "Нет папок для удаления.", w)
			return
		}

		msg := "Будут удалены следующие папки сезонов (кроме '" + settings.Season + "'):\n\n"
		for _, rel := range toDelete {
			msg += "  • " + rel + "\n"
		}
		msg += "\nПродолжить?"

		dialog.ShowConfirm("Подтверждение удаления", msg, func(confirm bool) {
			if !confirm {
				return
			}
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

	extContainer := container.NewHBox()
	for _, ext := range []string{".sto", ".rpy", ".blap", ".olap", ".ibt"} {
		extContainer.Add(extChecks[ext])
	}

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
	w.Show()

	a.Run()
}
