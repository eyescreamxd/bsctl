; Inno Setup script: per-user install of bsctl into %LOCALAPPDATA%\Programs\bsctl.
; Build: ISCC.exe /DAppVersion=0.8.0 installer\bsctl.iss (expects dist\bsctl.exe and
; dist\BlackSharkTray.exe built by CI). Settings and log stay in %LOCALAPPDATA%\bsctl.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif

[Setup]
AppId={{77AD927D-26B0-4553-9726-FE9ED0222661}
AppName=BlackShark Tray (bsctl)
AppVersion={#AppVersion}
AppVerName=BlackShark Tray {#AppVersion}
AppPublisher=eyescreamxd
AppPublisherURL=https://github.com/eyescreamxd/bsctl
AppUpdatesURL=https://github.com/eyescreamxd/bsctl/releases
DefaultDirName={localappdata}\Programs\bsctl
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\out
OutputBaseFilename=bsctl-setup-{#AppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ShowLanguageDialog=no
CloseApplications=no
UninstallDisplayIcon={app}\BlackSharkTray.exe
UninstallDisplayName=BlackShark Tray (bsctl)

[Languages]
Name: "ru"; MessagesFile: "compiler:Languages\Russian.isl"

[Tasks]
Name: "autostart"; Description: "Запускать трей при входе в Windows"

[Files]
Source: "..\dist\bsctl.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\dist\BlackSharkTray.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\third_party\rnnoise\*"; DestDir: "{app}\rnnoise"; Flags: ignoreversion recursesubdirs
Source: "..\third_party\dfn\*"; DestDir: "{app}\dfn"; Flags: ignoreversion recursesubdirs
Source: "..\README.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\BlackShark Tray"; Filename: "{app}\BlackSharkTray.exe"

[Run]
; Autostart only on an interactive install: a silent update keeps what the user chose in the tray menu.
Filename: "{app}\bsctl.exe"; Parameters: "autostart on"; Flags: runhidden; Tasks: autostart; Check: not WizardSilent
Filename: "{app}\BlackSharkTray.exe"; Description: "Запустить трей"; Flags: nowait postinstall

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/IM BlackSharkTray.exe /F"; Flags: runhidden; RunOnceId: "KillTray"
Filename: "{app}\bsctl.exe"; Parameters: "uninstall-cleanup"; Flags: runhidden; RunOnceId: "Cleanup"

[Code]
// Close every running tray (also a portable one from dist): it holds the exe
// and the single-instance mutex the new tray needs.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  rc: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM BlackSharkTray.exe', '', SW_HIDE, ewWaitUntilTerminated, rc);
  Sleep(2000);
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM BlackSharkTray.exe /F', '', SW_HIDE, ewWaitUntilTerminated, rc);
  Sleep(500);
  Result := '';
end;
