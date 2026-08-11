package database

import (
	"fmt"
	"io"
)

func RunCivilDateRepairPlanWithArgs(args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprintf(stdout, "Uso: admin civil-date-repair plan --database-url file:<caminho> --output plan.json\n")
			fmt.Fprintf(stdout, "\nGera um plano de reparo a partir da auditoria de datas civis.\n")
			fmt.Fprintf(stdout, "\nNOTA: plan ainda nao esta operacional nesta fase.\n")
			return 0
		}
	}
	fmt.Fprintln(stderr, "plan nao esta operacional nesta fase")
	return 1
}

func RunCivilDateRepairApplyWithArgs(args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprintf(stdout, "Uso: admin civil-date-repair apply --database-url file:<caminho> --plan plan.json --approved-plan-digest sha256:<hex> --backup-dir <dir>\n")
			fmt.Fprintf(stdout, "\nAplica reparos automaticos de datas civis com backup SQLite.\n")
			fmt.Fprintf(stdout, "\nNOTA: apply ainda nao esta implementado nesta fase.\n")
			return 0
		}
	}
	fmt.Fprintln(stderr, "apply nao esta implementado nesta fase")
	return 1
}
