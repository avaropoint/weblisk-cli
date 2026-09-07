package dispatch

// buildstatus.go — `weblisk build status`.
//
// The question this answers is the one nothing could: a build is running
// somewhere — started by Studio, or in a terminal that has since been closed —
// and you want to know whether it is progressing, stuck, or gone. Previously
// the only available evidence was whether a process existed, which is true
// right up until it is not, and says nothing about whether that process is
// doing anything.

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// BuildStatus prints what the build in root is doing.
func BuildStatus(root string, asJSON bool) error {
	v := ReadBuildVerdict(root)

	if asJSON {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		// A status command reports; it does not fail because the thing it
		// reports on failed. A caller reads the status field.
		return nil
	}

	fmt.Println()
	fmt.Printf("  Build status: %s\n", v.Status)
	fmt.Printf("  %s\n", v.Why)

	if v.Build != nil {
		st := v.Build.State
		fmt.Println()
		if st.Total > 0 {
			fmt.Printf("  Position:  file %d of %d — %s\n", st.Index, st.Total, st.File)
		}
		if v.ElapsedSeconds > 0 {
			fmt.Printf("  Running:   %s\n", roundDuration(time.Duration(v.ElapsedSeconds)*time.Second))
		}
		if !v.Build.LastEventAt.IsZero() {
			fmt.Printf("  Last word: %s ago", roundDuration(time.Duration(v.IdleSeconds)*time.Second))
			if st.Events > 0 {
				fmt.Printf(" (%s, %d bytes)", plural(st.Events, "event"), st.Bytes)
			}
			fmt.Println()
		}
		if st.Quota != "" {
			fmt.Printf("  Provider:  %s\n", st.Quota)
		}
	}

	// What to do about it, per status. A status with no next step is a status
	// somebody has to come and ask about.
	fmt.Println()
	switch v.Status {
	case "none":
		fmt.Println("  Nothing has been built here. Start one with: weblisk tenant create <name>")
	case "running":
		fmt.Println("  Nothing to do. Watch it with: weblisk build status --json")
	case "stalled":
		fmt.Println("  The provider has gone quiet. The build abandons a stalled call on its own;")
		fmt.Println("  if this persists, stop it and resume — every file already written is banked:")
		fmt.Println("    weblisk tenant create <name> --resume")
	case "died":
		fmt.Println("  Resume from what it banked:")
		fmt.Println("    weblisk tenant create <name> --resume")
	case "failed":
		fmt.Println("  Fix what the reason names, then resume:")
		fmt.Println("    weblisk tenant create <name> --resume")
	case "completed":
		fmt.Println("  Generation finished. Check the tenant itself with: weblisk server status")
	}
	fmt.Println()
	return nil
}

// BuildStatusHelp is the usage text.
func BuildStatusHelp() {
	fmt.Print(`
  Weblisk Build

  Usage:
    weblisk build status [--json]
      What the build in this directory is doing: running, stalled, died,
      completed or failed.

      Derived from two observed facts — whether the writing process is alive,
      and when the model last said anything — not from a timer. "Stalled" uses
      the same silence allowance the build itself uses to abandon a call, so a
      status and an abort cannot disagree.

`)
}

// buildStatusRoot is the directory to report on: the argument, or the cwd.
func buildStatusRoot(args []string) string {
	for _, a := range args {
		if a != "" && a[0] != '-' {
			return a
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

// HandleBuild dispatches the build subcommands.
func HandleBuild(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		BuildStatusHelp()
		return nil
	}
	switch args[0] {
	case "status":
		asJSON := false
		for _, a := range args[1:] {
			if a == "--json" {
				asJSON = true
			}
		}
		return BuildStatus(buildStatusRoot(args[1:]), asJSON)
	}
	return fmt.Errorf("unknown build command %q\n  Try: weblisk build status", args[0])
}
