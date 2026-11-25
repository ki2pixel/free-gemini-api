package benchmarkfixture2

import "testing"

func TestNewShape(t *testing.T) {
	circle := NewShape("circle", 2)
	if circle == nil || circle.Name() != "circle" || circle.Area() != 12 {
		t.Fatalf("circle = %#v", circle)
	}

	square := NewShape("square", 3)
	if square == nil || square.Name() != "square" || square.Area() != 9 {
		t.Fatalf("square = %#v", square)
	}
}
