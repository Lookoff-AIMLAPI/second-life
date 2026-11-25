; Per-user installer. No administrator rights, no PATH edits.
; Build: makensis /DVERSION=1.2.3 /DVERSION_NUM=1.2.3 /DBIN_DIR=<abs> /DOUT_FILE=<abs> installer.nsi

Unicode true
ManifestDPIAware true
RequestExecutionLevel user
SetCompressor /SOLID lzma
AllowSkipFiles off

!ifndef VERSION
  !error "VERSION is not defined"
!endif
!ifndef VERSION_NUM
  !error "VERSION_NUM is not defined (numeric x.y.z)"
!endif
!ifndef BIN_DIR
  !error "BIN_DIR is not defined (folder with slife.exe and slife-tray.exe)"
!endif
!ifndef OUT_FILE
  !error "OUT_FILE is not defined"
!endif

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "FileFunc.nsh"

!define APP_NAME "Second Life"
!define APP_DIR "$LOCALAPPDATA\Programs\SecondLife"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\SecondLife"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define RUN_VALUE "SecondLife"

Name "${APP_NAME}"
OutFile "${OUT_FILE}"
InstallDir "${APP_DIR}"
BrandingText "${APP_NAME} ${VERSION}"
ShowInstDetails nevershow
ShowUninstDetails nevershow

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\slife-tray.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APP_NAME}"

!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

VIProductVersion "${VERSION_NUM}.0"
VIAddVersionKey "ProductName" "${APP_NAME}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "${APP_NAME} setup"
VIAddVersionKey "CompanyName" "AIMLAPI"
VIAddVersionKey "LegalCopyright" "AIMLAPI"

; Ask the tray app to close, then force it. Only the tray process itself (no /T):
; a Claude Code window opened from the tray must survive.
!macro CloseTray
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM slife-tray.exe'
  Pop $0
  ${If} $0 != 128 ; 128: no such process
    Sleep 5000 ; a clean exit takes up to 4.5 s
    nsExec::Exec '"$SYSDIR\taskkill.exe" /F /IM slife-tray.exe'
    Pop $0
    Sleep 500
  ${EndIf}
!macroend

; slife.exe may be running a Claude Code session, so it is never killed. If the file
; is locked, ask the user to close it; a silent run stops with exit code 3.
!macro WaitForCli
  ${Do}
    ${IfNot} ${FileExists} "$INSTDIR\slife.exe"
      ${ExitDo}
    ${EndIf}
    ClearErrors
    FileOpen $0 "$INSTDIR\slife.exe" a
    ${IfNot} ${Errors}
      FileClose $0
      ${ExitDo}
    ${EndIf}
    ${If} ${Cmd} `MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "slife.exe is in use. Close the Claude Code windows started through Second Life, then press Retry." /SD IDCANCEL IDCANCEL`
      SetErrorLevel 3
      Quit
    ${EndIf}
  ${Loop}
!macroend

; The folder is fixed: /D= on the command line is ignored.
Function .onInit
  StrCpy $INSTDIR "${APP_DIR}"
  ${IfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "${APP_NAME} needs 64-bit Windows." /SD IDOK
    SetErrorLevel 2
    Quit
  ${EndIf}
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "${APP_NAME} needs Windows 10 or later." /SD IDOK
    SetErrorLevel 2
    Quit
  ${EndIf}
FunctionEnd

Function un.onInit
  StrCpy $INSTDIR "${APP_DIR}"
FunctionEnd

Section "Install"
  !insertmacro WaitForCli
  !insertmacro CloseTray

  SetOutPath "$INSTDIR"
  SetOverwrite on
  File "/oname=slife.exe" "${BIN_DIR}\slife.exe"
  File "/oname=slife-tray.exe" "${BIN_DIR}\slife-tray.exe"
  File "/oname=icon.ico" "${__FILEDIR__}\icon.ico"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortcut "$SMPROGRAMS\${APP_NAME}.lnk" "$INSTDIR\slife-tray.exe" "" "$INSTDIR\icon.ico" 0

  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\icon.ico"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "AIMLAPI"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "EstimatedSize" "$0"
SectionEnd

; Removes only what this installer and the app's own "Start at login" wrote.
; ~/.codex and ~/.second-life stay.
Section "Uninstall"
  !insertmacro WaitForCli
  !insertmacro CloseTray

  DeleteRegValue HKCU "${RUN_KEY}" "${RUN_VALUE}"
  Delete "$SMPROGRAMS\${APP_NAME}.lnk"
  Delete "$INSTDIR\slife.exe"
  Delete "$INSTDIR\slife-tray.exe"
  Delete "$INSTDIR\icon.ico"
  Delete "$INSTDIR\uninstall.exe"
  SetOutPath "$TEMP"
  RMDir "$INSTDIR" ; not /r: a folder with anything else in it stays
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd
