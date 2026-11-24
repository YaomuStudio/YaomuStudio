; YaomuStudio NSIS installer/uninstaller include entry.
;
; The file is prepended before MUI2.nsh. At top level only define macros,
; functions and variables. Uninstaller page callbacks live inside
; customUnWelcomePage, which expands in the MUI page-declaration area.
;
; The uninstall helper owns all data inspection/deletion. NSIS only renders
; the two default-off choices, confirms once, and stops program-directory
; removal when the helper fails. It never terminates or unregisters WSL.

!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "FileFunc.nsh"

; Installer-only: customInit/customInstall are not expanded in the uninstaller
; build. Declaring this only for installer builds keeps -WX warning 6001 away.
!ifndef BUILD_UNINSTALLER
  Var yaomuHadExistingInstall
!endif

!ifdef BUILD_UNINSTALLER
  Var yaomuPersonalCheckbox
  Var yaomuModelsCheckbox
  Var yaomuPersonalRequested
  Var yaomuModelsRequested
  Var yaomuModelsAvailable
  Var yaomuModelsSnapshot
  Var yaomuModelsSize
  Var yaomuInventoryIni
  Var yaomuApplyIni
  Var yaomuHelperExe

  ; Reject the stock template's whole-AppData bypass before any page or
  ; deletion. This also catches --delete-app-data=1.
  !macro customUnInit
    ${GetParameters} $R0
    ClearErrors
    ${GetOptions} $R0 "--delete-app-data" $R1
    ${IfNot} ${Errors}
      ${If} ${Silent}
        SetErrorLevel 2
        Quit
      ${EndIf}
      ${If} $LANGUAGE == 2052
        MessageBox MB_OK|MB_ICONSTOP "此卸载命令包含未授权的数据删除参数，已停止。请从 Windows 设置或应用目录重新运行卸载程序。"
      ${Else}
        MessageBox MB_OK|MB_ICONSTOP "This uninstall command contains an unauthorized data-deletion option and was stopped."
      ${EndIf}
      Quit
    ${EndIf}
  !macroend

  !macro customUnWelcomePage
    !insertmacro MUI_UNPAGE_WELCOME

    Function un.yaomuPrepareHelper
      InitPluginsDir
      StrCpy $yaomuHelperExe "$PLUGINSDIR\yaomu-uninstall-helper.exe"
      ${If} ${FileExists} "$yaomuHelperExe"
        Return
      ${EndIf}
      ClearErrors
      CopyFiles /SILENT "$INSTDIR\resources\core\yaomu-uninstall-helper.exe" "$yaomuHelperExe"
      ${IfNot} ${FileExists} "$yaomuHelperExe"
        ${If} ${Silent}
          SetErrorLevel 3
          Quit
        ${EndIf}
        ${If} $LANGUAGE == 2052
          MessageBox MB_OK|MB_ICONSTOP "无法准备卸载辅助工具，已停止卸载。"
        ${Else}
          MessageBox MB_OK|MB_ICONSTOP "The uninstall helper could not be prepared; uninstall was stopped."
        ${EndIf}
        Quit
      ${EndIf}
    FunctionEnd

    Function un.yaomuRunInventory
      StrCpy $yaomuModelsAvailable "0"
      StrCpy $yaomuModelsSize ""
      StrCpy $yaomuModelsSnapshot ""
      StrCpy $yaomuInventoryIni "$TEMP\yaomu-uninstall-inventory.ini"
      Delete "$yaomuInventoryIni"
      Call un.yaomuPrepareHelper
      nsExec::ExecToStack '"$yaomuHelperExe" inventory --user-data "$APPDATA\@yaomustudio\desktop" --install-dir "$INSTDIR" --documents "$DOCUMENTS" --result "$yaomuInventoryIni"'
      Pop $0
      Pop $1
      ${If} ${FileExists} "$yaomuInventoryIni"
        ReadINIStr $R0 "$yaomuInventoryIni" "uninstall" "models_known"
        ReadINIStr $R1 "$yaomuInventoryIni" "uninstall" "models_bytes"
        ReadINIStr $R2 "$yaomuInventoryIni" "uninstall" "models_size_display"
        ReadINIStr $yaomuModelsSnapshot "$yaomuInventoryIni" "uninstall" "snapshot"
        ${If} $R0 == "1"
        ${AndIf} $R1 != "0"
          StrCpy $yaomuModelsAvailable "1"
          StrCpy $yaomuModelsSize "$R2"
        ${EndIf}
      ${EndIf}
    FunctionEnd

    Function un.yaomuDataPageCreate
      ${If} ${isUpdated}
        Abort
      ${EndIf}

      ; The app is per-user; use the invoking user's AppData/Documents even in
      ; a per-machine uninstall. The uninstaller restores the previous context.
      ${If} $installMode == "all"
        SetShellVarContext current
      ${EndIf}

      Call un.yaomuRunInventory

      ${If} $LANGUAGE == 2052
        !insertmacro MUI_HEADER_TEXT "本机数据" "选择是否随卸载删除本机数据"
      ${Else}
        !insertmacro MUI_HEADER_TEXT "Local data" "Choose whether to delete local data"
      ${EndIf}

      nsDialogs::Create 1018
      Pop $0
      ${If} $0 == error
        Abort
      ${EndIf}

      ${If} $LANGUAGE == 2052
        ${NSD_CreateLabel} 0u 0u 280u 24u "项目和模型会留在这台电脑上，下次安装可以继续使用。"
        Pop $0
        ${NSD_CreateCheckbox} 0u 30u 280u 22u "删除个人数据和设置"
        Pop $yaomuPersonalCheckbox
        ${NSD_OnClick} $yaomuPersonalCheckbox un.yaomuPersonalClick
        ${NSD_CreateLabel} 0u 50u 280u 24u "项目、字幕与编辑记录、媒体副本、日志和界面设置；不包含模型和运行环境。"
        Pop $0
        ${If} $yaomuModelsAvailable == "1"
          StrCpy $R0 "同时删除已下载模型（约 $yaomuModelsSize）"
        ${Else}
          StrCpy $R0 "同时删除已下载模型"
        ${EndIf}
        ${NSD_CreateCheckbox} 0u 80u 280u 22u "$R0"
        Pop $yaomuModelsCheckbox
        ${NSD_OnClick} $yaomuModelsCheckbox un.yaomuModelsClick
        ${NSD_CreateLabel} 0u 100u 280u 36u "保留模型，下次安装就不用重新下载。删除后使用相关功能需要重新下载，首次使用会多等一会儿。运行环境会保留。"
        Pop $0
        ${If} $yaomuModelsAvailable != "1"
          ${NSD_CreateLabel} 0u 136u 280u 20u "暂时无法确认哪些模型可以安全删除，本次会先保留。"
          Pop $0
        ${EndIf}
        ${NSD_CreateLabel} 0u 158u 280u 36u "程序目录里的导出文件会先备份到文档目录；备份会保留，需要时可自行清理。"
        Pop $0
      ${Else}
        ${NSD_CreateLabel} 0u 0u 280u 24u "Projects and models stay on this computer so you can continue after reinstalling."
        Pop $0
        ${NSD_CreateCheckbox} 0u 30u 280u 22u "Delete personal data and settings"
        Pop $yaomuPersonalCheckbox
        ${NSD_OnClick} $yaomuPersonalCheckbox un.yaomuPersonalClick
        ${NSD_CreateLabel} 0u 50u 280u 24u "Projects, subtitles and edits, media copies, logs and UI settings; models and runtime are not included."
        Pop $0
        ${If} $yaomuModelsAvailable == "1"
          StrCpy $R0 "Also delete downloaded models (about $yaomuModelsSize)"
        ${Else}
          StrCpy $R0 "Also delete downloaded models"
        ${EndIf}
        ${NSD_CreateCheckbox} 0u 80u 280u 22u "$R0"
        Pop $yaomuModelsCheckbox
        ${NSD_OnClick} $yaomuModelsCheckbox un.yaomuModelsClick
        ${NSD_CreateLabel} 0u 100u 280u 36u "Keeping models avoids downloading them again after reinstalling. If deleted, related features will download them again and take longer the first time. The runtime stays."
        Pop $0
        ${If} $yaomuModelsAvailable != "1"
          ${NSD_CreateLabel} 0u 136u 280u 20u "The app cannot safely identify downloadable models right now; they will be kept."
          Pop $0
        ${EndIf}
        ${NSD_CreateLabel} 0u 158u 280u 36u "Exported files in the program directory are backed up to Documents first; the backup stays until you remove it."
        Pop $0
      ${EndIf}

      ${If} $yaomuModelsAvailable != "1"
        EnableWindow $yaomuModelsCheckbox 0
      ${EndIf}
      ${If} $yaomuPersonalRequested == "1"
        ${NSD_SetState} $yaomuPersonalCheckbox ${BST_CHECKED}
      ${EndIf}
      ${If} $yaomuModelsAvailable == "1"
      ${AndIf} $yaomuModelsRequested == "1"
        ${NSD_SetState} $yaomuModelsCheckbox ${BST_CHECKED}
      ${EndIf}

      ${If} $installMode == "all"
        SetShellVarContext all
      ${EndIf}
      nsDialogs::Show
    FunctionEnd

    Function un.yaomuPersonalClick
      ${NSD_GetState} $yaomuPersonalCheckbox $0
      StrCpy $yaomuPersonalRequested $0
    FunctionEnd

    Function un.yaomuModelsClick
      ${NSD_GetState} $yaomuModelsCheckbox $0
      StrCpy $yaomuModelsRequested $0
    FunctionEnd

    Function un.yaomuDataPageLeave
      ${NSD_GetState} $yaomuPersonalCheckbox $0
      StrCpy $yaomuPersonalRequested $0
      ${If} $yaomuModelsAvailable == "1"
        ${NSD_GetState} $yaomuModelsCheckbox $0
        StrCpy $yaomuModelsRequested $0
      ${Else}
        StrCpy $yaomuModelsRequested "0"
      ${EndIf}
    FunctionEnd

    UninstPage custom un.yaomuDataPageCreate un.yaomuDataPageLeave
  !macroend
!endif

; Installer-only onboarding reset. Detection happens before the old version is
; removed; fresh non-update installs start onboarding, manual install-over and
; updates preserve the existing state.
!macro customInstall
  ${IfNot} ${isUpdated}
    ${If} $yaomuHadExistingInstall != "1"
      ${If} $installMode == "all"
        SetShellVarContext current
      ${EndIf}
      CreateDirectory "$APPDATA\@yaomustudio\desktop"
      Delete "$APPDATA\@yaomustudio\desktop\onboarding.json"
      FileOpen $0 "$APPDATA\@yaomustudio\desktop\onboarding.json" w
      FileWrite $0 '{"schema_version":1,"completed":false}'
      FileClose $0
      ${If} $installMode == "all"
        SetShellVarContext all
      ${EndIf}
    ${EndIf}
  ${EndIf}
!macroend

!macro customInit
  StrCpy $yaomuHadExistingInstall "0"
  IfFileExists "$INSTDIR\${APP_EXECUTABLE_FILENAME}" 0 +2
    StrCpy $yaomuHadExistingInstall "1"
!macroend
