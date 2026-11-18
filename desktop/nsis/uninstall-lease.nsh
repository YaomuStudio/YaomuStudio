; 卸载时释放本机 Runner 租约。
; 正常退出在 WSL 子进程未能确认结束时会留下租约，下一次启动会一直等待资源。
; 卸载是用户明确拆除这次安装：只删除租约目录，不删除项目、模型、数据库或 WSL 镜像。
!macro customUnInstall
  StrCmp $APPDATA "" yaomu_uninstall_lease_done
  RMDir /r "$APPDATA\@yaomustudio\desktop\core-data\runtime\runner-lease"
  yaomu_uninstall_lease_done:
!macroend
