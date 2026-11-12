# NOCTURNE

Писательская мастерская для Mac и Windows: рукописи, план глав, герои и мир книги, сюжетные линии, арки и хронология, мудборд, музыка для письма, виджеты и режим фокуса.

Программа сделана Елизаветой Левинской. Сейчас идёт бета-тестирование, последняя версия — **1.0 RC3.8.20**.

## Скачать

- **Mac** — [NOCTURNE_Mac_beta_RC3.8.20_beta.zip](https://github.com/efremovaelis7-lab/Nocturne/raw/main/downloads/NOCTURNE_Mac_beta_RC3.8.20_beta.zip). Для macOS 12 и новее, для Mac с процессорами Apple (M1–M4) и Intel.
- **Windows** — [NOCTURNE_Windows_beta_RC3.8.20.zip](https://github.com/efremovaelis7-lab/Nocturne/raw/main/downloads/NOCTURNE_Windows_beta_RC3.8.20.zip). Для Windows 10 и 11.

Что нового в этой версии и контрольные суммы — в [downloads/README.md](downloads/README.md).

В каждом архиве есть инструкция по установке. Программы пока не подписаны платным сертификатом, поэтому при первом запуске macOS и Windows покажут предупреждение. Как его пройти, написано в инструкции.

## Где хранятся книги

Все книги хранятся на компьютере, в папке «Документы → NOCTURNE». Раз в час программа сама делает резервную копию, а полный архив можно скачать кнопкой внизу бокового меню.

NOCTURNE работает без интернета и никуда не отправляет ни тексты, ни статистику. Аккаунтов, рекламы и аналитики нет. Интерфейс обращается только к собственному серверу библиотеки на этом же компьютере (адрес `127.0.0.1`). В интернет программа выходит, только если открыть сохранённую в ней ссылку, например на плейлист во внешнем плеере.

## Что лежит в репозитории

| Папка | Что внутри |
|---|---|
| `app/` | Интерфейс программы: `index.html` и `app.js`. Один и тот же для Mac, Windows и Android. |
| `desktop/core/` | Сервер библиотеки на Go (только стандартная библиотека): чтение и сохранение `library.json`, резервные копии, архивы, музыка. |
| `desktop/cmd/nocturne-server/` | Сервер для окна Mac. |
| `desktop/cmd/nocturne-win/` | Программа для Windows: запускает сервер и открывает окно через Microsoft Edge. |
| `desktop/mac/` | Окно для Mac (C и Objective-C, WKWebView), меню, иконка, `Info.plist`. |
| `desktop/tools/` | Иконка для Windows и проверка готового `.exe`. |
| `fonts-licenses/` | Лицензии встроенных шрифтов. |
| `android/` | Приложение для Android (тестовая сборка): окно WebView и тот же сервер библиотеки. Подробнее — в [android/README.md](android/README.md). |
| `downloads/` | Готовые архивы для Mac и Windows. |

## Сборка

Нужен Go 1.23 или новее. Для Windows ещё нужен Python 3 с Pillow, для Mac — clang, `ld64.lld` и `llvm-lipo` из LLVM 18. Xcode не нужен. Собирать можно на Linux или на Mac.

```sh
cd desktop
./build-win.sh ../app ../build/windows   # NOCTURNE.exe и NOCTURNE-ARM.exe
./build-mac.sh ../app ../build/mac       # NOCTURNE.app (Apple и Intel)
```

`NOCTURNE.exe` и окно Mac из выпуска RC3.8.20 собираются из этого кода байт в байт. Контрольные суммы SHA-256 архивов лежат в `downloads/SHA256SUMS.txt`.

## Авторские права

© 2026 Елизавета Левинская. Все права защищены.

Код открыт для ознакомления. Копировать, изменять, распространять или использовать его целиком или частями без письменного разрешения автора нельзя.

Встроенные шрифты (Alegreya, Caveat, Comfortaa, Cormorant Garamond, EB Garamond, Inter, Lora, Marck Script, Montserrat, Nunito, Playfair Display, PT Sans, PT Serif, Raleway, Rubik) распространяются по лицензии SIL Open Font License 1.1. Тексты лицензий лежат в папке `fonts-licenses/`.

## Связь с автором

Ошибки, вопросы и пожелания можно присылать:

- в Telegram: [@Elevi17](https://t.me/Elevi17);
- во ВКонтакте: [vk.ru/elizzevi17](https://vk.ru/elizzevi17).

Если что-то сломалось, к сообщению полезно приложить снимок экрана и пару слов о том, что происходило перед ошибкой.
