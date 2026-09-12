package main

import "image/color"

// Provider marks. The badge's fonts are 7-bit ASCII, so a logo has to be
// built from rectangles and pixels: Claude is its radiating burst, Antigravity
// the four-pointed Gemini spark, Codex a hexagon ring (OpenAI's mark reduced
// to its outline). Each fills a markSize box and is selected by the provider
// letter carried in the frame.
const (
	markSize = 12
	markGap  = 4

	// Burst: a 2x2 hub with four straight arms and shorter diagonals.
	burstHub      = 2
	burstArm      = 4
	burstDiagonal = 3

	// Spark: half the rows are listed, the rest mirror them.
	sparkRows = markSize / 2

	// Hexagon ring: flat top and bottom edges of markSize-2*hexEdgeInset
	// pixels, hexSlope diagonal steps per corner, straight sides between.
	hexEdgeInset = 3
	hexSlope     = 3
)

var (
	colClaudeMark = color.RGBA{217, 119, 87, 255}
	colAgyMark    = color.RGBA{120, 140, 250, 255}

	// OpenAI's mark is monochrome; light grey keeps it apart from the white
	// value column without disappearing into the labels.
	colCodexMark = color.RGBA{220, 220, 220, 255}

	// Row widths of the spark's top half, tip first — the concave taper is
	// what separates it from a plain diamond at this size.
	sparkWidths = [sparkRows]int16{2, 2, 3, 4, 6, 10}
)

// drawProviderMark paints the mark of a provider letter and reports whether it
// drew anything; rows without a provider (the "+N more" summary) get none.
func drawProviderMark(prov byte, x, y int16) bool {
	switch prov {
	case provClaude:
		drawClaudeMark(x, y)
	case provAgy:
		drawAgyMark(x, y)
	case provCodex:
		drawCodexMark(x, y)
	default:
		return false
	}

	return true
}

func drawClaudeMark(x, y int16) {
	c := colClaudeMark

	// Top-left corner of the hub; arms hang off its four sides.
	hubX, hubY := x+markSize/2-1, y+markSize/2-1

	display.FillRectangle(hubX, hubY, burstHub, burstHub, c)
	display.FillRectangle(hubX, hubY-burstArm, burstHub, burstArm, c)
	display.FillRectangle(hubX, hubY+burstHub, burstHub, burstArm, c)
	display.FillRectangle(hubX-burstArm, hubY, burstArm, burstHub, c)
	display.FillRectangle(hubX+burstHub, hubY, burstArm, burstHub, c)

	for i := int16(1); i <= burstDiagonal; i++ {
		display.SetPixel(hubX-i, hubY-i, c)
		display.SetPixel(hubX+burstHub-1+i, hubY-i, c)
		display.SetPixel(hubX-i, hubY+burstHub-1+i, c)
		display.SetPixel(hubX+burstHub-1+i, hubY+burstHub-1+i, c)
	}
}

func drawAgyMark(x, y int16) {
	c := colAgyMark

	for i, w := range sparkWidths {
		row := int16(i)
		left := x + (markSize-w)/2

		display.FillRectangle(left, y+row, w, 1, c)
		display.FillRectangle(left, y+markSize-1-row, w, 1, c)
	}
}

func drawCodexMark(x, y int16) {
	c := colCodexMark
	edgeW := int16(markSize - 2*hexEdgeInset)

	// Flat top and bottom edges.
	display.FillRectangle(x+hexEdgeInset, y, edgeW, 1, c)
	display.FillRectangle(x+hexEdgeInset, y+markSize-1, edgeW, 1, c)

	// Corner diagonals walking out from the edges to the sides.
	for i := int16(1); i <= hexSlope; i++ {
		display.SetPixel(x+hexEdgeInset-i, y+i, c)
		display.SetPixel(x+markSize-1-hexEdgeInset+i, y+i, c)
		display.SetPixel(x+hexEdgeInset-i, y+markSize-1-i, c)
		display.SetPixel(x+markSize-1-hexEdgeInset+i, y+markSize-1-i, c)
	}

	// Straight sides between the corners.
	display.FillRectangle(x, y+hexSlope, 1, markSize-2*hexSlope, c)
	display.FillRectangle(x+markSize-1, y+hexSlope, 1, markSize-2*hexSlope, c)
}
