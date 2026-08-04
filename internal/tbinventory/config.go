package tbinventory

import "os"

func LoadConfig() Config {
	base := os.Getenv("TB_API_BASE")
	if base == "" {
		base = "https://open.teambition.com/api"
	}
	return Config{os.Getenv("TB_APP_ID"), os.Getenv("TB_APP_SECRET"), os.Getenv("TB_ORG_ID"), os.Getenv("TB_OPERATOR_ID"), base}
}
