// Command uninstall-helper is the narrow Windows uninstall helper.
//
// It is invoked by NSIS before the program directory is removed. The command
// never edits business state; it reads completed install records, validates
// app-owned targets and performs only explicitly selected cleanup.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/uninstall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: yaomu-uninstall-helper inventory|apply [flags]")
		os.Exit(2)
	}
	mode := os.Args[1]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	userData := flags.String("user-data", "", "Electron userData directory")
	installDir := flags.String("install-dir", "", "application install directory")
	documents := flags.String("documents", "", "user Documents directory")
	resultPath := flags.String("result", "", "INI result path")
	personal := flags.Bool("personal", false, "delete whitelisted personal data")
	models := flags.Bool("models", false, "delete app-owned model weights")
	snapshot := flags.String("snapshot", "", "inventory snapshot")
	flashcliRoot := flags.String("flashcli-root", "", "test-only FlashCLI root")
	wslDistro := flags.String("wsl-distro", "", "WSL distribution")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	if *userData == "" || *installDir == "" || *documents == "" || *resultPath == "" {
		fmt.Fprintln(os.Stderr, "user-data, install-dir, documents and result are required")
		os.Exit(2)
	}
	if *flashcliRoot != "" && os.Getenv("YAOMU_UNINSTALL_TEST") != "1" {
		fmt.Fprintln(os.Stderr, "flashcli-root is test-only")
		os.Exit(2)
	}
	options := uninstall.Options{
		UserDataDir:  *userData,
		InstallDir:   *installDir,
		DocumentsDir: *documents,
		Personal:     *personal,
		Models:       *models,
		Snapshot:     *snapshot,
		FlashCLIRoot: *flashcliRoot,
		WSLDistro:    *wslDistro,
		Now:          time.Now,
	}
	var result uninstall.Result
	var err error
	switch mode {
	case "inventory":
		result, err = uninstall.Inventory(context.Background(), options)
	case "apply":
		result, err = uninstall.Apply(context.Background(), options)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode")
		os.Exit(2)
	}
	code := 0
	if err != nil {
		code = 5
		var failure *uninstall.Failure
		if errors.As(err, &failure) {
			code = failure.Code
			if failure.MessageKey != "" {
				result.MessageKey = failure.MessageKey
			}
		}
		if result.SchemaVersion == 0 {
			result = uninstall.Result{SchemaVersion: uninstall.SchemaVersion, Status: "failed"}
		}
		result.Status = "failed"
		if result.MessageKey == "" {
			result.MessageKey = "uninstall-error-failed"
		}
		fmt.Fprintln(os.Stderr, err.Error())
	}
	if writeErr := writeResult(*resultPath, result); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr.Error())
		code = 2
	}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
	os.Exit(code)
}

func writeResult(path string, result uninstall.Result) error {
	return uninstall.WriteINI(path, result)
}
