; 卸载时释放 Runner 租约，并在用户明确确认后删除项目数据。
; WSL 运行环境和模型权重始终保留；外部原始素材不在此脚本范围内。
Var /GLOBAL YaomuDeleteData

!macro customUnInit
  StrCpy $YaomuDeleteData "0"
  IfSilent yaomu_uninstall_init_done

  MessageBox MB_YESNOCANCEL|MB_ICONQUESTION|MB_DEFBUTTON2 "是否删除 YaomuStudio 的本机项目数据？$\r$\n$\r$\n选择「是」会永久删除项目、数据库、素材副本、字幕、声纹、模型登记、缓存、偏好、本地令牌和程序目录 outputs 中的导出文件。$\r$\nWSL 运行环境和模型权重始终保留，重新安装可复用。$\r$\n选择「否」（推荐）只卸载应用，保留数据；导出文件会复制到文档/YaomuStudio Exports。$\r$\n选择「取消」退出卸载。" IDYES yaomu_uninstall_confirm_delete IDNO yaomu_uninstall_init_done
  Quit

  yaomu_uninstall_confirm_delete:
  MessageBox MB_YESNO|MB_ICONEXCLAMATION|MB_DEFBUTTON2 "项目数据删除后无法恢复。WSL 运行环境和模型权重仍会保留。确认继续？" IDYES yaomu_uninstall_mark_delete IDNO yaomu_uninstall_init_done

  yaomu_uninstall_mark_delete:
  StrCpy $YaomuDeleteData "1"

  yaomu_uninstall_init_done:
!macroend

!macro customUnInstall
  StrCmp $APPDATA "" yaomu_uninstall_lease_done
  RMDir /r "$APPDATA\@yaomustudio\desktop\core-data\runtime\runner-lease"

  StrCmp $YaomuDeleteData "1" yaomu_uninstall_delete_data yaomu_uninstall_keep_data

  yaomu_uninstall_keep_data:
  IfFileExists "$INSTDIR\outputs\*.*" 0 yaomu_uninstall_lease_done
  CreateDirectory "$DOCUMENTS\YaomuStudio Exports"
  CopyFiles /SILENT "$INSTDIR\outputs\*.*" "$DOCUMENTS\YaomuStudio Exports"
  Goto yaomu_uninstall_lease_done

  yaomu_uninstall_delete_data:
  RMDir /r "$INSTDIR\outputs"
  RMDir /r "$APPDATA\@yaomustudio\desktop\core-data"
  Delete "$APPDATA\@yaomustudio\desktop\desktop-preferences.json"

  yaomu_uninstall_lease_done:
!macroend
