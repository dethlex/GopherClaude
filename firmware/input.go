package main

// inputEvent is what a board's input layer hands the core: the badge derives
// them from buttons and the accelerometer, the display from touch gestures.
// The core never sees a button or a finger.
type inputEvent uint8

const (
	evNone         inputEvent = iota
	evViewNext                // dashboard: next view; session list: cursor down
	evViewPrev                // dashboard: previous view; session list: cursor up
	evListPageNext            // session list: jump a page forward
	evListPagePrev            // session list: jump a page back
	evPageToggle              // dashboard <-> session list
	evSelectRow               // session list: highlight row (input.row = index into the filtered list)
	evFocus                   // open the highlighted / alerting session on the host
	evMute                    // silence the current alert without opening anything
	evSoundToggle             // global beep kill-switch
	evWake                    // any activity that should postpone or leave standby
	evRest                    // badge laid flat: mute and darken until picked up
	evUnrest                  // badge picked up again
	evOpenSettings            // display: settings menu (arrives with the provisioning stage)
)

// input is one polled event; row is meaningful for evSelectRow only.
type input struct {
	kind inputEvent
	row  int
}
