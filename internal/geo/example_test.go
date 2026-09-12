package geo

import "fmt"

// ExampleDecodeRegion — миниатюрный регион: flat-блоки нулевой высоты и один
// complex-блок с высотой 1000 и открытыми направлениями.
func ExampleDecodeRegion() {
	b := newRegionBuilder()
	b.addComplex(flatComplexBlock(1000, NSWEAll))
	data := b.build()

	reg, err := DecodeRegion(16, 10, data)
	if err != nil {
		panic(err)
	}
	// Мировая точка в complex-блоке региона (16, 10).
	cell := reg.CellAt(WorldToGeoX(-131008), WorldToGeoY(-262064))
	z, nswe := cell.Nearest(960)
	fmt.Println(z, nswe == NSWEAll, cell.LowerZ(960), cell.HigherZ(960))
	// Output: 1000 true 960 1000
}
