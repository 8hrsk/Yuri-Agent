# Windows offline runner

## Что это такое

Yuri offline runner — локальный PowerShell-процесс, не использующий GitHub
Actions. Он делает исходящее соединение с Git remote, опрашивает ветку `dev`,
и для нового commit запускает проверки непосредственно на Windows PC.

Это **не air-gapped build**: `git fetch`, первая загрузка Go modules, `npm ci`
и установка фиксированного Wails CLI требуют доступа в интернет. Белый IP и
входящие подключения не нужны.

Pipeline выполняет:

1. whitespace и `gofmt` checks;
2. `go vet` и полный Go test suite;
3. `npm ci`, ESLint, Stylelint, bridge-contract check, TypeScript и Vitest;
4. production frontend build;
5. native Wails `windows/amd64` build;
6. onboarding и voice UI smoke в изолированном test profile;
7. сохранение `yuri.exe`, SHA-256, metadata, лога и JSON-статуса.

## Модель работы

Runner хранит всё в отдельном каталоге, по умолчанию
`C:\YuriOfflineRunner`:

```text
C:\YuriOfflineRunner
├── runner\                 # стабильная копия PowerShell runner
├── runner.config.json
├── checkout\               # только runner-managed Git checkout
├── tools\                  # фиксированный Wails v2.15.0
├── logs\
├── artifacts\<commit>\yuri.exe
├── status.json             # последний poll/build result
└── latest-success.json     # последний успешный artifact, не затирается failure
```

На каждом build runner выполняет `git reset --hard` и `git clean -ffdx`
**только внутри `checkout`**. Не храните там ручные изменения, настройки или
пользовательские данные Yuri.

## Требования к Windows PC

- Windows 10/11 x64 с интерактивной пользовательской сессией;
- Git for Windows;
- Go `1.25.x` (версия проекта указана в `go.mod`);
- Node.js `22.x` и npm;
- Microsoft WebView2 Runtime;
- PowerShell 5.1 или новее.

Проверьте окружение в обычном PowerShell:

```powershell
git --version
go version
node --version
npm --version
```

WebView2 нужен для запуска Wails UI smoke. Wails рекомендует проверить
Windows dependencies командой `wails doctor`; runner также выполняет doctor
перед сборкой. Актуальные требования Wails v2:
[Installation](https://v2.wails.io/docs/gettingstarted/installation/).

## Подготовка GitHub

После создания GitHub repository отправьте ветки:

```powershell
git remote add origin https://github.com/YOUR_ACCOUNT/Yuri-Agent.git
git push -u origin main
git switch -c dev
git push -u origin dev
```

Для public repository дополнительная авторизация не нужна. Для private
repository сначала выполните обычный `git clone` или `git fetch` под тем же
Windows user, чтобы Git Credential Manager сохранил доступ. Не записывайте PAT
в URL или `runner.config.json`. Сам runner отключает интерактивные Git prompts,
чтобы скрытая scheduled task не зависла в ожидании ввода.

## Установка runner

Клонируйте repository на Windows или перенесите исходники любым безопасным
способом. Из корня проекта откройте обычный PowerShell и выполните:

```powershell
Set-ExecutionPolicy -Scope Process Bypass

.\scripts\windows-runner\Install-YuriOfflineRunner.ps1 `
  -RepositoryUrl "https://github.com/YOUR_ACCOUNT/Yuri-Agent.git" `
  -Branch "dev" `
  -InstallRoot "C:\YuriOfflineRunner"
```

Installer копирует runner в стабильный каталог и создаёт JSON config. Он не
устанавливает системную службу и не требует admin-доступа.

Сначала обязательно выполните один foreground run:

```powershell
& "C:\YuriOfflineRunner\runner\Invoke-YuriOfflineRunner.ps1" `
  -ConfigPath "C:\YuriOfflineRunner\runner.config.json" `
  -Once
```

Первый запуск дольше последующих: загружаются repository, Go modules, npm
packages и Wails CLI. Окна Yuri дважды кратковременно появятся во время UI
smoke и автоматически закроются. Успешный executable находится по пути из
`C:\YuriOfflineRunner\latest-success.json`.

## Автозапуск через Task Scheduler

После успешного foreground run повторите installer с флагом:

```powershell
.\scripts\windows-runner\Install-YuriOfflineRunner.ps1 `
  -RepositoryUrl "https://github.com/YOUR_ACCOUNT/Yuri-Agent.git" `
  -Branch "dev" `
  -InstallRoot "C:\YuriOfflineRunner" `
  -RegisterScheduledTask
```

Создаётся задача `Yuri Offline Windows CI`, запускающая один постоянный polling
process при входе текущего пользователя. Используется `Interactive` logon,
поскольку настоящий UI smoke требует desktop session. Runner и сборка идут без
повышения прав. Если Task Scheduler вернёт `Access denied`, запустите только
installer один раз из PowerShell от администратора; сама задача всё равно
регистрируется с `RunLevel Limited`.

Запустить задачу немедленно:

```powershell
Start-ScheduledTask -TaskName "Yuri Offline Windows CI"
```

Остановить polling:

```powershell
Stop-ScheduledTask -TaskName "Yuri Offline Windows CI"
```

Удалить только scheduled task, сохранив checkout, логи и artifacts:

```powershell
Unregister-ScheduledTask -TaskName "Yuri Offline Windows CI" -Confirm
```

## Ручной build и повтор failure

Однократно проверить текущий `dev`:

```powershell
& "C:\YuriOfflineRunner\runner\Invoke-YuriOfflineRunner.ps1" `
  -ConfigPath "C:\YuriOfflineRunner\runner.config.json" `
  -Once
```

Принудительно повторить commit, даже если он уже проверен:

```powershell
& "C:\YuriOfflineRunner\runner\Invoke-YuriOfflineRunner.ps1" `
  -ConfigPath "C:\YuriOfflineRunner\runner.config.json" `
  -Once -Force
```

Failed commit автоматически повторяется через `retryFailedAfterMinutes`.
Passed commit повторно не собирается до нового push или `-Force`.

Запустить последний успешный artifact:

```powershell
$result = Get-Content "C:\YuriOfflineRunner\latest-success.json" -Raw | ConvertFrom-Json
Start-Process $result.artifactPath
```

## Диагностика

```powershell
Get-Content "C:\YuriOfflineRunner\status.json" -Raw
Get-Content "C:\YuriOfflineRunner\latest-success.json" -Raw
Get-Content "C:\YuriOfflineRunner\logs\<log-file>.log" -Tail 200
Get-ScheduledTaskInfo -TaskName "Yuri Offline Windows CI"
```

Статусы `status.json`:

| result | Значение |
| --- | --- |
| `running` | pipeline выполняется |
| `passed` | tests, build и UI smoke прошли |
| `failed` | commit собрался/проверился с ошибкой; смотрите `logPath` |
| `poll-error` | Git/network/config error до начала pipeline |

Если ветка `dev` ещё не создана, будет `poll-error`; после первого push runner
подхватит её автоматически. Если private repository пытается запросить пароль
в скрытом scheduled process, остановите задачу, выполните foreground run и
завершите вход через Git Credential Manager.

## Security boundary

Push в `dev` означает выполнение кода из этой ветки на Windows PC. Используйте
отдельного непривилегированного Windows user без production secrets, не
запускайте runner от администратора и не разрешайте недоверенным pull requests
автоматически попадать в `dev`. Runner не читает `.env` и не требует GitHub PAT,
но тестируемый код технически имеет права запустившего его Windows user.

После изменения самих runner-скриптов повторно выполните installer из свежего
trusted checkout: работающая копия намеренно не обновляет себя кодом из `dev`.
