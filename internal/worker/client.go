package worker

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

func Call(socket string, req Request) (Response, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var res Response
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return Response{}, err
	}
	if !res.OK {
		if res.Error != nil {
			return res, fmt.Errorf("%s: %s", res.Error.Code, res.Error.Message)
		}
		return res, fmt.Errorf("daemon request failed")
	}
	return res, nil
}
