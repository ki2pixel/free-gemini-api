package benchmarkfixture2

func NewShape(kind string, size int) Shape {
	if kind == "circle" {
		return Circle{Radius: size}
	}
	return nil
}
