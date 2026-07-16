module github.com/florinel-chis/etoro-go/backtestsource

go 1.26.5

require (
	github.com/florinel-chis/etoro-go v0.0.0
	github.com/florinel-chis/gobacktest v0.0.0
)

// Local development replaces; repointed to tagged releases at publish time.
replace github.com/florinel-chis/gobacktest => /Users/fch/repos/gobacktest

replace github.com/florinel-chis/etoro-go => ../
