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

//go:embed logo.png
var logoPNG []byte

// logoIcon is the same icon drawn ahead of time for the sidebar, where an SVG
// stroke this thick does not survive being scaled to 22 pixels.
var logoIcon = fyne.NewStaticResource("testease-logo.png", logoPNG)

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
