package main

import (
	"bytes"
	"net/http"

	"github.com/hashicorp/go-hclog"
)

func CallDeregisterHook(logger hclog.Logger, endpoint string, data []byte) error {
	req, err := http.NewRequest("DELETE", endpoint, bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error("error calling service deregister HTTP hook", "error", err, "url", endpoint)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			logger.Error("service deregister HTTP hook returned non-200 status code",
				"status_code", resp.StatusCode, "url", endpoint)
		} else {

			logger.Info("service deregister HTTP hook called successfully", "url", endpoint)
		}
	}

	return err
}
