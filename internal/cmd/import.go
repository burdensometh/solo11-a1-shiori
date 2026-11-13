package cmd

import (
	"fmt"
	"os"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/spf13/cobra"
)

func importCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import source-file",
		Short: "Import bookmarks from HTML file in Netscape Bookmark format",
		Args:  cobra.ExactArgs(1),
		Run:   importHandler,
	}

	cmd.Flags().BoolP("generate-tag", "t", false, "Auto generate tag from bookmark's category")

	return cmd
}

func importHandler(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()
	_, deps := initShiori(ctx, cmd)

	// Parse flags
	generateTag := cmd.Flags().Changed("generate-tag")

	// If user doesn't specify, ask if tag need to be generated
	if !generateTag {
		var submit string
		fmt.Print("Add parents folder as tag? (y/N): ")
		fmt.Scanln(&submit)

		generateTag = submit == "y"
	}

	// Open bookmark's file
	srcFile, err := os.Open(args[0])
	if err != nil {
		cError.Printf("Failed to open %s: %v\n", args[0], err)
		os.Exit(1)
	}
	defer srcFile.Close()

	db := deps.Database()

	// Parse and import the bookmarks. On a rerun only the entries that
	// were not successfully imported in the previous run of the same
	// source are processed.
	_, saved, err := executeImport(ctx, db, args[0], model.ImportKindNetscape, func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
		return parseNetscapeImport(ctx, db, srcFile, generateTag, filter)
	})
	if err != nil {
		cError.Printf("Import failed: %v\n", err)
		os.Exit(1)
	}

	// Print imported bookmark
	fmt.Println()
	printBookmarks(saved...)
}
