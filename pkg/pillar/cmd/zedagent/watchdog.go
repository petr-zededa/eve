package zedagent

import (
	"os"
)

const (
	//WatchdogDevicePath is the Watchdog device file path
	WatchdogDevicePath = "/dev/watchdog"
)

func getWatchdogPresent(ctx *zedagentContext) bool {
	_, err := os.Stat(WatchdogDevicePath)
	if err != nil {
		//No Watchdog found on this system
		return false
	}
	return true
}
