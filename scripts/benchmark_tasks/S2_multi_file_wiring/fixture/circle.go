package benchmarkfixture2

type Circle struct{ Radius int }

func (c Circle) Area() int { return 3 * c.Radius * c.Radius }

func (c Circle) Name() string { return "circle" }
