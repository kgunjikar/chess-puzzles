package site

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/kgunjikar/chess-puzzles/internal/puzzle"
)

var glyph = map[byte]string{'k': "♚", 'q': "♛", 'r': "♜", 'b': "♝", 'n': "♞", 'p': "♟︎"}

var pieceName = map[byte]string{'k': "king", 'q': "queen", 'r': "rook", 'b': "bishop", 'n': "knight", 'p': "pawn"}

// BoardHTML renders the position with the same markup the browser script
// uses, so the board shows before JavaScript loads (and for crawlers).
func BoardHTML(fen string, flipped bool) template.HTML {
	board := puzzle.BoardFromFEN(fen)
	var sb strings.Builder
	for r := 0; r < 8; r++ {
		for f := 0; f < 8; f++ {
			rank, file := 8-r, f
			if flipped {
				rank, file = r+1, 7-f
			}
			sq := fmt.Sprintf("%c%d", 'a'+file, rank)
			color := "d"
			if (file+rank)%2 == 1 {
				color = "l"
			}
			fmt.Fprintf(&sb, `<div class="sq %s" data-sq="%s">`, color, sq)
			if f == 0 {
				fmt.Fprintf(&sb, `<span class="coord r">%d</span>`, rank)
			}
			if r == 7 {
				fmt.Fprintf(&sb, `<span class="coord f">%c</span>`, 'a'+file)
			}
			if pc, ok := board[sq]; ok {
				lower, side := pc|0x20, "b"
				if pc >= 'A' && pc <= 'Z' {
					side = "w"
				}
				name := "white"
				if side == "b" {
					name = "black"
				}
				fmt.Fprintf(&sb, `<span class="pc %s" role="img" aria-label="%s %s on %s">%s</span>`,
					side, name, pieceName[lower], sq, glyph[lower])
			}
			sb.WriteString(`</div>`)
		}
	}
	return template.HTML(sb.String())
}
