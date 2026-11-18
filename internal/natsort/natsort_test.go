package natsort

import "testing"

func TestLess(t *testing.T) {
	for _, c := range [][2]string{{"season 2", "season 10"}, {"s01e09", "s01e10"}, {"s01e99", "s01e100"}, {"a", "b"}, {"a", "a1"}, {"2", "a"}, {"e02", "e3"}} {
		if !Less(c[0], c[1]) || Less(c[1], c[0]) {
			t.Fatalf("%q debe ir antes que %q", c[0], c[1])
		}
	}
	if Less("x", "x") {
		t.Fatal("un nombre no va antes que sí mismo")
	}
}
