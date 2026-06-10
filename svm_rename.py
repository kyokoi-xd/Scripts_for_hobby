import os
import re
import tkinter as tk
from tkinter import filedialog, scrolledtext, messagebox
from pathlib import Path
from threading import Thread
from datetime import datetime

class RenameApp:
    def __init__(self, root):
        self.root = root
        self.root.title("Удаление версии из имён .svm файлов")
        self.root.geometry("700x500")
        self.root.resizable(True, True)

        self.folder_path = tk.StringVar()

        tk.Label(root, text="Выберите папку для сканирования:", font=("Arial", 10)).pack(pady=(10, 5))

        frame_path = tk.Frame(root)
        frame_path.pack(fill=tk.X, padx=10, pady=5)
        tk.Entry(frame_path, textvariable=self.folder_path, width=60).pack(side=tk.LEFT, fill=tk.X, expand=True)
        tk.Button(frame_path, text="Обзор...", command=self.select_folder).pack(side=tk.RIGHT, padx=5)

        frame_buttons = tk.Frame(root)
        frame_buttons.pack(pady=10)
        tk.Button(frame_buttons, text="Предпросмотр (dry-run)", command=self.start_dry_run, bg="#f0f0f0", width=20).pack(side=tk.LEFT, padx=5)
        tk.Button(frame_buttons, text="Переименовать", command=self.start_rename, bg="#d9ead3", width=20).pack(side=tk.LEFT, padx=5)

        tk.Label(root, text="Лог операций:", font=("Arial", 10)).pack(anchor=tk.W, padx=10)
        self.log_area = scrolledtext.ScrolledText(root, wrap=tk.WORD, height=20, width=80)
        self.log_area.pack(fill=tk.BOTH, expand=True, padx=10, pady=5)

        self.status_var = tk.StringVar(value="Готов")
        status_bar = tk.Label(root, textvariable=self.status_var, bd=1, relief=tk.SUNKEN, anchor=tk.W)
        status_bar.pack(side=tk.BOTTOM, fill=tk.X)

    def select_folder(self):
        folder = filedialog.askdirectory(title="Выберите папку с .svm файлами")
        if folder:
            self.folder_path.set(folder)
            self.log(f"Выбрана папка: {folder}")

    def log(self, message, level="INFO"):
        timestamp = datetime.now().strftime("%H:%M:%S")
        self.log_area.insert(tk.END, f"[{timestamp}] {message}\n")
        self.log_area.see(tk.END)
        self.root.update_idletasks()

    def remove_version(self, filename: str) -> str | None:
        """
        Удаляет версию вида v1.2.3 (или 1.2.3) в конце имени.
        После удаления обрезает завершающий символ '_', если он есть.
        """
        pattern = r'(.*?)(?:v?\d+\.\d+\.\d+)$'
        match = re.match(pattern, filename, re.IGNORECASE)
        if match:
            new_name = match.group(1)
            # Убираем нижнее подчёркивание в конце, если оно появилось
            if new_name.endswith('_'):
                new_name = new_name[:-1]
            # Если имя не изменилось или стало пустым — возвращаем None
            if not new_name or new_name == filename:
                return None
            return new_name
        return None

    def process_files(self, dry_run: bool):
        folder = self.folder_path.get().strip()
        if not folder:
            messagebox.showwarning("Нет папки", "Пожалуйста, выберите папку.")
            return
        if not os.path.isdir(folder):
            messagebox.showerror("Ошибка", f"Папка не существует: {folder}")
            return

        self.status_var.set("Работаю..." if not dry_run else "Предпросмотр...")
        self.log("=" * 60)
        self.log(f"{'ПРЕДПРОСМОТР (dry-run)' if dry_run else 'ПЕРЕИМЕНОВАНИЕ'}")
        self.log(f"Папка: {folder}")

        renamed = 0
        skipped = 0
        errors = 0

        root_path = Path(folder)
        svm_files = list(root_path.rglob("*.svm"))
        self.log(f"Найдено .svm файлов: {len(svm_files)}")

        for file_path in svm_files:
            stem = file_path.stem
            new_stem = self.remove_version(stem)

            if new_stem is None:
                self.log(f"❌ Пропуск (версия не найдена или имя не изменилось): {file_path.name}")
                skipped += 1
                continue

            new_name = new_stem + file_path.suffix
            new_path = file_path.parent / new_name

            if new_path.exists():
                self.log(f"⚠️ Конфликт: {file_path.name} -> {new_name} (уже существует, пропущено)")
                skipped += 1
                continue

            if dry_run:
                self.log(f"[DRY RUN] {file_path.name} -> {new_name}")
                renamed += 1
            else:
                try:
                    file_path.rename(new_path)
                    self.log(f"✅ {file_path.name} -> {new_name}")
                    renamed += 1
                except Exception as e:
                    self.log(f"🔥 Ошибка при переименовании {file_path.name}: {e}")
                    errors += 1

        self.log(f"\nРезультат: переименовано {renamed}, пропущено {skipped}, ошибок {errors}")
        self.status_var.set(f"Готов. Переименовано: {renamed}, пропущено: {skipped}")
        if not dry_run and renamed > 0:
            messagebox.showinfo("Завершено", f"Готово!\nПереименовано файлов: {renamed}\nПропущено: {skipped}")

    def start_dry_run(self):
        Thread(target=lambda: self.process_files(dry_run=True), daemon=True).start()

    def start_rename(self):
        if messagebox.askyesno("Подтверждение", "Вы уверены, что хотите переименовать файлы? Рекомендуется сделать резервную копию."):
            Thread(target=lambda: self.process_files(dry_run=False), daemon=True).start()

if __name__ == "__main__":
    root = tk.Tk()
    app = RenameApp(root)
    root.mainloop()