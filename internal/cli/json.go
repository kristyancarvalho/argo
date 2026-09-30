package cli

import (
	"encoding/json"
	"io"

	"github.com/kristyancarvalho/argo/internal/diagnostic"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

const JSONSchemaVersion = 1

type JSONDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Data          any    `json:"data"`
}

func WriteJSON(output io.Writer, kind string, data any) error {
	return json.NewEncoder(output).Encode(JSONDocument{
		SchemaVersion: JSONSchemaVersion,
		Kind:          kind,
		Data:          data,
	})
}

func safeJSONDownload(download ipc.Download) ipc.Download {
	download.URL = diagnostic.URL(download.URL)
	download.Error = diagnostic.Text(download.Error)
	return download
}

func safeJSONStatus(status ipc.Status) ipc.Status {
	status.Network.Error = diagnostic.Text(status.Network.Error)
	status.Traffic.Error = diagnostic.Text(status.Traffic.Error)
	return status
}
