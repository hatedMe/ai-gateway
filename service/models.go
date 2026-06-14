package service

import (
    "encoding/json"
    "fmt"
    "io"
    "traecli-api-proxy/config"
)

type TraeConfigInfo struct {
    ConfigName string `json:"config_name"`
    Usage      string `json:"usage"`
}

func FetchTraeModels(auth string) ([]TraeConfigInfo, error) {
    if config.Cfg == nil {
        return nil, fmt.Errorf("config not loaded")
    }

    req := GetHTTPClient().R().
        SetHeader("Content-Type", "application/json").
        SetHeader("Authorization", auth).
        SetBody(`{"function":"chat"}`)

    for k, v := range config.Cfg.Trae.Headers {
        req = req.SetHeader(k, v)
    }

    resp, err := req.Post("https://console.enterprise.trae.cn/api/ide/v1/cli/get_config_list")
    if err != nil {
        return nil, fmt.Errorf("failed to fetch models: %w", err)
    }
    defer resp.RawBody().Close()

    body, err := io.ReadAll(resp.RawBody())
    if err != nil {
        return nil, fmt.Errorf("failed to read response body: %w", err)
    }

    if resp.StatusCode() >= 400 {
        return nil, fmt.Errorf("failed to fetch models: status code %d, body: %s", resp.StatusCode(), string(body))
    }

    if len(body) == 0 {
        return nil, fmt.Errorf("empty response body from get_config_list")
    }

    var traeResp struct {
        Message string           `json:"message"`
        ConfigInfoList []TraeConfigInfo `json:"config_info_list"`
    }
    
    var traeRespData struct {
        Data struct {
            ConfigInfoList []TraeConfigInfo `json:"config_info_list"`
        } `json:"data"`
    }

    if err := json.Unmarshal(body, &traeResp); err == nil && len(traeResp.ConfigInfoList) > 0 {
        return traeResp.ConfigInfoList, nil
    }

    if err := json.Unmarshal(body, &traeRespData); err == nil && len(traeRespData.Data.ConfigInfoList) > 0 {
        return traeRespData.Data.ConfigInfoList, nil
    }

    var raw map[string]interface{}
    if err := json.Unmarshal(body, &raw); err != nil {
        return nil, fmt.Errorf("failed to parse models response: %w. body: %s", err, string(body))
    }

    if traeResp.ConfigInfoList != nil {
        return traeResp.ConfigInfoList, nil
    }

    return nil, fmt.Errorf("could not find config_info_list in response. body: %s", string(body))
}

