package main

func colorHistogram(pixels []byte) [48]float64 {
	var histogram [48]float64
	for offset := 0; offset+2 < len(pixels); offset += 3 {
		for channel := 0; channel < 3; channel++ {
			histogram[channel*16+int(pixels[offset+channel])/16]++
		}
	}
	total := float64(len(pixels))
	if total > 0 {
		for index := range histogram {
			histogram[index] /= total
		}
	}
	return histogram
}
