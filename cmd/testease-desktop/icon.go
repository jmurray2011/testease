package main

import (
	_ "embed"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

//go:embed icon.svg
var iconSVG []byte

// appIcon is the window's and the taskbar's icon.
var appIcon = fyne.NewStaticResource("testease.svg", iconSVG)

// sectionIcons stand beside the section names.
func sectionIcons() [sections]fyne.Resource {
	return [sections]fyne.Resource{
		theme.HomeIcon(), theme.ListIcon(), theme.ContentCopyIcon(), theme.GridIcon(),
		theme.DocumentIcon(), theme.MediaPlayIcon(), theme.ComputerIcon(), theme.SettingsIcon(),
	}
}

// activityLabel counts the jobs still going beside Activity.
func activityLabel(running int) string {
	if running == 0 {
		return sectionNames[sectionActivity]
	}
	return fmt.Sprintf("%s  %d", sectionNames[sectionActivity], running)
}
