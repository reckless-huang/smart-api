package migrate

import (
	"bytes"
	"fmt"
	"github.com/go-admin-team/go-admin-core/sdk"
	"github.com/go-admin-team/go-admin-core/sdk/pkg"
	"strconv"
	"text/template"
	"time"

	"github.com/go-admin-team/go-admin-core/config/source/file"
	"github.com/spf13/cobra"

	"github.com/go-admin-team/go-admin-core/sdk/config"
	"smart-api/cmd/migrate/migration"
	_ "smart-api/cmd/migrate/migration/version"
	_ "smart-api/cmd/migrate/migration/version-local"
	"smart-api/common/database"
	"smart-api/common/models"
)

var (
	configYml string
	generate  bool
	smartApi  bool
	host      string
	StartCmd  = &cobra.Command{
		Use:     "migrate",
		Short:   "Initialize the database",
		Example: "smart-api migrate -c config/settings.yml",
		Run: func(cmd *cobra.Command, args []string) {
			run()
		},
	}
)

// fixme 在您看不见代码的时候运行迁移，我觉得是不安全的，所以编译后最好不要去执行迁移
func init() {
	StartCmd.PersistentFlags().StringVarP(&configYml, "config", "c", "config/settings.yml", "Start server with provided configuration file")
	StartCmd.PersistentFlags().BoolVarP(&generate, "generate", "g", false, "generate migration file")
	StartCmd.PersistentFlags().BoolVarP(&smartApi, "smartApi", "a", false, "generate smart migration file")
	StartCmd.PersistentFlags().StringVarP(&host, "domain", "d", "*", "select tenant host")
}

func run() {

	if !generate {
		fmt.Println(`start init`)
		//1. 读取配置
		config.Setup(
			file.NewSource(file.WithPath(configYml)),
			initDB,
		)
	} else {
		fmt.Println(`generate migration file`)
		_ = genFile()
	}
}

func migrateModel() error {
	if host == "" {
		host = "*"
	}
	db := sdk.Runtime.GetDbByKey(host)
	if db == nil {
		if len(sdk.Runtime.GetDb()) == 1 && host == "*" {
			for k, v := range sdk.Runtime.GetDb() {
				db = v
				host = k
				break
			}
		}
	}
	if db == nil {
		return fmt.Errorf("未找到数据库配置")
	}
	if config.DatabasesConfig[host].Driver == "mysql" {
		//初始化数据库时候用
		db.Set("gorm:table_options", "ENGINE=InnoDB CHARSET=utf8mb4")
	}
	err := db.Debug().AutoMigrate(&models.Migration{})
	if err != nil {
		return err
	}
	migration.Migrate.SetDb(db.Debug())
	migration.Migrate.Migrate()
	return err
}
func initDB() {
	//3. 初始化数据库链接
	database.Setup()
	//4. 数据库迁移
	fmt.Println("数据库迁移开始")
	_ = migrateModel()
	fmt.Println(`数据库基础数据初始化成功`)
}

func genFile() error {
	t1, err := template.ParseFiles("template/migrate.template")
	if err != nil {
		return err
	}
	m := map[string]string{}
	m["GenerateTime"] = strconv.FormatInt(time.Now().UnixNano()/1e6, 10)
	m["Package"] = "version_local"
	if smartApi {
		m["Package"] = "version"
	}
	var b1 bytes.Buffer
	err = t1.Execute(&b1, m)
	if smartApi {
		pkg.FileCreate(b1, "./cmd/migrate/migration/version/"+m["GenerateTime"]+"_migrate.go")
	} else {
		pkg.FileCreate(b1, "./cmd/migrate/migration/version-local/"+m["GenerateTime"]+"_migrate.go")
	}
	return nil
}
