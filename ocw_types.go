// ocw
package main

import (
	oapi "github.com/Nemo08/goosmapi"
)

type OsmResponse struct {
	Channel string
	Comment string
	Bbox    oapi.BoundsBox
	Resp    oapi.ChangesetInfo
}

type Area struct {
	TelegrammChannel string         `json:"channel"`
	Comment          string         `json:"comment"`
	Bbox             oapi.BoundsBox `json:"boundsbox"`
	UpdateTime       int            `json:"updatetime"`
	LatestChangeset  int64          `json:"latestchangeset"`
}

type JsonAreaConfig struct {
	Areas []Area `json:"areas"`
}

type WriteToConfig struct {
	id              int
	LatestChangeset int64
}
