// Package api は api/openapi.yaml から生成したサーバーのインターフェースとクライアント。
//
// 仕様を変えたら go generate ./... で再生成する。api.gen.go は編集しないこと。
package api

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../api/openapi.yaml
