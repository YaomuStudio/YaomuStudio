; YaomuStudio uninstall entry.
;
; The bundled helper is the only component allowed to inspect or delete
; personal data/model weights. It runs before the template removes $INSTDIR;
; a nonzero helper exit stops the uninstall. WSL is never terminated or
; unregistered here.
!include "${__FILEDIR__}\installer.nsh"

!macro customUnInstall
  ${If} $installMode == "all"
    SetShellVarContext current
  ${EndIf}

  ; Do not delete the Runner lease here: CHECK_APP_RUNNING and the helper's
  ; process check do not prove that a WSL/Runner execution resource has exited.
  ; A retained stale lease is the safe direction; it must never be used as a
  ; substitute for a real Core/Runner stop proof.
  Call un.yaomuPrepareHelper
  StrCpy $yaomuApplyIni "$TEMP\yaomu-uninstall-apply.ini"
  Delete "$yaomuApplyIni"
  ${IfNot} ${isUpdated}
    ${If} $yaomuPersonalRequested == "1"
    ${OrIf} $yaomuModelsRequested == "1"
      ${If} $yaomuPersonalRequested == "1"
        ${If} $yaomuModelsRequested == "1"
          StrCpy $R0 "确认删除个人数据和已下载模型？删除后无法恢复。"
        ${Else}
          StrCpy $R0 "确认删除个人数据？删除后无法恢复。"
        ${EndIf}
      ${Else}
        StrCpy $R0 "确认删除已下载模型？删除后无法恢复。"
      ${EndIf}
      ${If} $LANGUAGE != 2052
        ${If} $yaomuPersonalRequested == "1"
          ${If} $yaomuModelsRequested == "1"
            StrCpy $R0 "Delete personal data and downloaded models? This cannot be undone."
          ${Else}
            StrCpy $R0 "Delete personal data? This cannot be undone."
          ${EndIf}
        ${Else}
          StrCpy $R0 "Delete downloaded models? This cannot be undone."
        ${EndIf}
      ${EndIf}
      MessageBox MB_YESNO|MB_ICONEXCLAMATION|MB_DEFBUTTON2 "$R0" /SD IDNO IDNO yaomu_uninstall_skip_data
      Goto yaomu_uninstall_apply
      yaomu_uninstall_skip_data:
      StrCpy $yaomuPersonalRequested "0"
      StrCpy $yaomuModelsRequested "0"
      yaomu_uninstall_apply:
    ${EndIf}
  ${EndIf}

  ; Backup-only on updates/manual install-over; destructive flags only after
  ; the non-update confirmation above. The helper still verifies the backup.
  SetDetailsPrint none
  StrCpy $R0 '"$yaomuHelperExe" apply --user-data "$APPDATA\@yaomustudio\desktop" --install-dir "$INSTDIR" --documents "$DOCUMENTS" --result "$yaomuApplyIni"'
  ${If} $yaomuPersonalRequested == "1"
    StrCpy $R0 "$R0 --personal"
  ${EndIf}
  ${If} $yaomuModelsRequested == "1"
    StrCpy $R0 "$R0 --models"
  ${EndIf}
  ${If} $yaomuPersonalRequested == "1"
  ${OrIf} $yaomuModelsRequested == "1"
    StrCpy $R0 "$R0 --snapshot $yaomuModelsSnapshot"
  ${EndIf}
  nsExec::ExecToStack '$R0'
  Pop $0
  Pop $1
  SetDetailsPrint lastused
  ${If} $0 != 0
    StrCpy $R1 ""
    ${If} ${FileExists} "$yaomuApplyIni"
      ReadINIStr $R1 "$yaomuApplyIni" "uninstall" "message_key"
    ${EndIf}
    ${If} $R1 == "uninstall-error-backup-failed"
      StrCpy $R2 "导出文件备份失败，卸载已停止。"
      ${If} $LANGUAGE != 2052
        StrCpy $R2 "Backing up exported files failed; uninstall was stopped."
      ${EndIf}
    ${ElseIf} $R1 == "uninstall-error-snapshot-stale"
      StrCpy $R2 "本机数据在确认后发生变化，卸载已停止。"
      ${If} $LANGUAGE != 2052
        StrCpy $R2 "Local data changed after confirmation; uninstall was stopped."
      ${EndIf}
    ${ElseIf} $R1 == "uninstall-error-app-running"
      StrCpy $R2 "应用仍在运行，卸载已停止。"
      ${If} $LANGUAGE != 2052
        StrCpy $R2 "The app is still running; uninstall was stopped."
      ${EndIf}
    ${ElseIf} $R1 == "uninstall-error-models-unknown"
      StrCpy $R2 "无法安全确认模型目标，卸载已停止。"
      ${If} $LANGUAGE != 2052
        StrCpy $R2 "Model targets could not be safely confirmed; uninstall was stopped."
      ${EndIf}
    ${Else}
      StrCpy $R2 "清理未完成，卸载已停止。请重试，或保留数据后再卸载。"
      ${If} $LANGUAGE != 2052
        StrCpy $R2 "Cleanup did not complete; uninstall was stopped. Retry, or keep the data and uninstall later."
      ${EndIf}
    ${EndIf}
    ${If} ${Silent}
      SetErrorLevel 3
    ${Else}
      MessageBox MB_OK|MB_ICONSTOP "$R2"
    ${EndIf}
    ${If} $installMode == "all"
      SetShellVarContext all
    ${EndIf}
    Quit
  ${EndIf}

  ${If} $installMode == "all"
    SetShellVarContext all
  ${EndIf}
!macroend
