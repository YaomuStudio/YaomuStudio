package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	modernsqlite "modernc.org/sqlite"
)

// backupMaker 是 modernc 驱动连接暴露的在线备份能力，
// 通过 database/sql 的 Conn.Raw 拿到底层驱动连接后做类型断言。
type backupMaker interface {
	NewBackup(dstURI string) (*modernsqlite.Backup, error)
}

// backupSQLite 用 SQLite 在线备份 API 把 db 一致地复制到 dstPath。
// dstPath 若已存在会先删除，避免把旧备份文件当作新目标。
func backupSQLite(ctx context.Context, db *sql.DB, dstPath string) error {
	if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale backup: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.Raw(func(driverConn any) error {
		bm, ok := driverConn.(backupMaker)
		if !ok {
			return fmt.Errorf("driver connection %T does not support backup", driverConn)
		}
		b, err := bm.NewBackup(dstPath)
		if err != nil {
			return err
		}
		for {
			more, err := b.Step(-1) // -1 一次性复制全部剩余页
			if err != nil {
				_ = b.Finish() // 主错误已确定，这里只做尽力清理
				return err
			}
			if !more {
				return b.Finish() // Finish 同时关闭目标连接
			}
		}
	})
}

// verifyBackup 校验备份文件可打开且结构完整。
func verifyBackup(backupPath string) error {
	bdb, err := sql.Open("sqlite", backupPath)
	if err != nil {
		return err
	}
	defer bdb.Close()

	var result string
	if err := bdb.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check returned %q", result)
	}
	return nil
}
