// Command dockerfile-parser поднимает веб-сервис визуализации зависимостей сервисов.
package main

import (
	"flag"
	"log"
	"net/http"

	"dockerfile-parser/web"
)

func main() {
	addr := flag.String("addr", ":8080", "адрес прослушивания HTTP")
	flag.Parse()

	log.Printf("DockerFile-Parser слушает http://localhost%s", *addr)
	if err := http.ListenAndServe(*addr, web.NewServer()); err != nil {
		log.Fatal(err)
	}
}
