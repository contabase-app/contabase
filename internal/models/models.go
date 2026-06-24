package models

import (
	"encoding/json"
	"strings"
	"time"
)

type User struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Email              string `json:"email"`
	PasswordHash       string `json:"-"`
	DefaultWorkspaceID string `json:"default_workspace_id"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type Workspace struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Type        string  `json:"type"`
	ThemeToken  string  `json:"theme_token"`
	CompanyName *string `json:"company_name"`
	CNPJCPF     *string `json:"cnpj_cpf"`
	Address     *string `json:"address"`
	Phone       *string `json:"phone"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

const (
	WorkspaceTypePersonal = "personal"
	WorkspaceTypeBusiness = "business"
)

type WorkspaceMember struct {
	WorkspaceID          string   `json:"workspace_id"`
	UserID               string   `json:"user_id"`
	Role                 string   `json:"role"`
	JoinedAt             int64    `json:"joined_at"`
	CustomPermissionsRaw string   `json:"custom_permissions"`
	CustomPermissions    []string `json:"custom_permissions_list"`
}

const (
	RoleAdmin   = "ADMIN"
	RoleManager = "MANAGER"
	RoleUser    = "USER"
)

const (
	PermissionBackupExport   = "backup.export"
	PermissionContactsDelete = "contacts.delete"
	PermissionAdminAuditRead = "admin.auditoria.read"
	PermissionWorkspaceAdmin = "workspace.admin"
	PermissionWorkspaceEdit  = "workspace.edit"
	PermissionReportsView    = "reports.view"
)

var defaultManagerPermissions = map[string]struct{}{
	"dashboard:read":         {},
	"transactions:read":      {},
	"transactions:create":    {},
	"transactions:update":    {},
	"transactions:delete":    {},
	"invoices:status_update": {},
	"goals:write":            {},
	"config:read":            {},
	"config:write":           {},
	"members:read":           {},
	"members:write":          {},
	"profile:read":           {},
	"profile:write":          {},
	"attachment:read":        {},
	PermissionContactsDelete: {},
	PermissionReportsView:    {},
}

var defaultUserPermissions = map[string]struct{}{
	"dashboard:read":      {},
	"transactions:read":   {},
	"transactions:create": {},
	"profile:read":        {},
	"profile:write":       {},
	"attachment:read":     {},
}

var allowedCustomPermissions = map[string]struct{}{}

func HasPermission(member *WorkspaceMember, permission string) bool {
	if member == nil {
		return false
	}
	perm := normalizePermissionToken(permission)
	if perm == "" {
		return false
	}
	role := strings.ToUpper(strings.TrimSpace(member.Role))
	if role == RoleAdmin {
		return true
	}
	switch role {
	case RoleManager:
		if _, ok := defaultManagerPermissions[perm]; ok {
			return true
		}
	case RoleUser:
		if _, ok := defaultUserPermissions[perm]; ok {
			return true
		}
	}
	return false
}

func IsAllowedCustomPermission(permission string) bool {
	_, ok := allowedCustomPermissions[normalizePermissionToken(permission)]
	return ok
}

func (m *WorkspaceMember) PermissionList() []string {
	if m == nil {
		return nil
	}
	if len(m.CustomPermissions) > 0 {
		return normalizePermissionList(m.CustomPermissions)
	}
	m.CustomPermissions = normalizePermissionList(ParsePermissionList(m.CustomPermissionsRaw))
	return m.CustomPermissions
}

func (m *WorkspaceMember) SetCustomPermissions(permissions []string) {
	if m == nil {
		return
	}
	normalized := normalizePermissionList(permissions)
	raw, err := json.Marshal(normalized)
	if err != nil {
		m.CustomPermissionsRaw = "[]"
		m.CustomPermissions = nil
		return
	}
	m.CustomPermissionsRaw = string(raw)
	m.CustomPermissions = normalized
}

func ParsePermissionList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return normalizePermissionList(out)
	}
	return normalizePermissionList(strings.Split(raw, ","))
}

func PermissionListToJSON(permissions []string) string {
	normalized := normalizePermissionList(permissions)
	if len(normalized) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func normalizePermissionList(permissions []string) []string {
	if len(permissions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(permissions))
	out := make([]string, 0, len(permissions))
	for _, item := range permissions {
		token := normalizePermissionToken(item)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func normalizePermissionToken(permission string) string {
	return strings.ToLower(strings.TrimSpace(permission))
}

type Account struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Color          string `json:"color"`
	Icon           string `json:"icon"`
	ProviderSlug   string `json:"provider_slug"`
	InitialBalance int64  `json:"initial_balance"`
	CurrentBalance int64  `json:"current_balance"`
	SortOrder      int64  `json:"sort_order"`
	ArchivedAt     *int64 `json:"archived_at,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (a *Account) IsArchived() bool {
	return a.ArchivedAt != nil
}

const (
	AccountTypeChecking   = "CHECKING"
	AccountTypeSavings    = "SAVINGS"
	AccountTypeInvestment = "INVESTMENT"
	AccountTypeWallet     = "WALLET"
	AccountTypeCreditCard = "CREDIT_CARD"
)

func AccountTypeLabel(typ string) string {
	switch typ {
	case AccountTypeChecking:
		return "Conta Corrente"
	case AccountTypeSavings:
		return "Poupança"
	case AccountTypeInvestment:
		return "Investimento"
	case AccountTypeWallet:
		return "Carteira / Dinheiro"
	case AccountTypeCreditCard:
		return "Cartão de Crédito"
	default:
		return "Conta"
	}
}

type CreditCard struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	ClosingDay  int64  `json:"closing_day"`
	DueDay      int64  `json:"due_day"`
	CreditLimit int64  `json:"credit_limit"`
}

type Category struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Type        string `json:"type"`
	MacroGroup  string `json:"macro_group"`
	ParentID    string `json:"parent_id"`
	IsFixed     bool   `json:"is_fixed"`
	CreatedAt   int64  `json:"created_at"`
}

const (
	CategoryTypeExpense = "EXPENSE"
	CategoryTypeIncome  = "INCOME"
)

type Invoice struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Reference   string `json:"reference"`
	ClosingDate int64  `json:"closing_date"`
	DueDate     int64  `json:"due_date"`
	Status      string `json:"status"`
	PaidAt      *int64 `json:"paid_at"`
	PaidAmount  *int64 `json:"paid_amount"`
	CreatedAt   int64  `json:"created_at"`
}

type InvoicePayment struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	InvoiceID     string  `json:"invoice_id"`
	AccountID     string  `json:"account_id"`
	TransactionID *string `json:"transaction_id"`
	AmountCents   int64   `json:"amount_cents"`
	PaidAt        int64   `json:"paid_at"`
	Note          *string `json:"note"`
	Source        string  `json:"source"`
	ReversedAt    *int64  `json:"reversed_at"`
	CreatedBy     *string `json:"created_by"`
	CreatedAt     int64   `json:"created_at"`
}

const (
	InvoiceStatusOpen   = "OPEN"
	InvoiceStatusClosed = "CLOSED"
	InvoiceStatusPaid   = "PAID"
)

type Transaction struct {
	ID                   string  `json:"id"`
	WorkspaceID          string  `json:"workspace_id"`
	UserID               string  `json:"user_id"`
	AccountID            string  `json:"account_id"`
	DestinationAccountID *string `json:"destination_account_id"`
	CategoryID           *string `json:"category_id"`
	InvoiceID            *string `json:"invoice_id"`
	InvoiceOverrideID    *string `json:"invoice_override_id"`
	Type                 string  `json:"type"`
	Amount               int64   `json:"amount"`
	Date                 int64   `json:"date"`
	Description          string  `json:"description"`
	InstallmentNumber    int64   `json:"installment_number"`
	TotalInstallments    int64   `json:"total_installments"`
	ParentID             *string `json:"parent_id"`
	RecurringRuleID      *string `json:"recurring_rule_id"`
	RecurrenceSequence   *int64  `json:"recurrence_sequence"`
	Status               string  `json:"status"`
	DueDate              *int64  `json:"due_date"`
	ContactID            *string `json:"contact_id"`
	CreatedAt            int64   `json:"created_at"`
	UpdatedAt            int64   `json:"updated_at"`
}

const (
	TransactionTypeExpense  = "EXPENSE"
	TransactionTypeIncome   = "INCOME"
	TransactionTypeTransfer = "TRANSFER"
)

const (
	PaymentStatusPaid        = "paid"
	PaymentStatusPending     = "pending"
	TransactionStatusPaid    = "paid"
	TransactionStatusPending = "pending"
)

type Contact struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Document    string `json:"document"`
	Type        string `json:"type"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	CreatedAt   int64  `json:"created_at"`
}

const (
	ContactTypeClient = "client"
	ContactTypeVendor = "vendor"
)

type RecurringRule struct {
	ID                   string  `json:"id"`
	WorkspaceID          string  `json:"workspace_id"`
	UserID               string  `json:"user_id"`
	AccountID            string  `json:"account_id"`
	DestinationAccountID *string `json:"destination_account_id"`
	CategoryID           *string `json:"category_id"`
	Type                 string  `json:"type"`
	Amount               int64   `json:"amount"`
	Description          string  `json:"description"`
	StartDate            int64   `json:"start_date"`
	Frequency            string  `json:"frequency"`
	DefaultPaymentStatus string  `json:"default_payment_status"`
	Active               bool    `json:"active"`
	TotalOccurrences     *int64  `json:"total_occurrences"`
	GeneratedUntil       *int64  `json:"generated_until"`
	CreatedAt            int64   `json:"created_at"`
	UpdatedAt            int64   `json:"updated_at"`
}

const (
	RecurrenceFrequencyDaily      = "DAILY"
	RecurrenceFrequencyWeekly     = "WEEKLY"
	RecurrenceFrequencyBiweekly   = "BIWEEKLY"
	RecurrenceFrequencyMonthly    = "MONTHLY"
	RecurrenceFrequencyBimonthly  = "BIMONTHLY"
	RecurrenceFrequencyQuarterly  = "QUARTERLY"
	RecurrenceFrequencySemiannual = "SEMIANNUAL"
	RecurrenceFrequencyAnnual     = "ANNUAL"
)

type CostLimit struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	CategoryID       string `json:"category_id"`
	MaxAmountMonthly int64  `json:"max_amount_monthly"`
}

type Box struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	CategoryID      string `json:"category_id"`
	TargetAmount    int64  `json:"target_amount"`
	MonthlyRecharge int64  `json:"monthly_recharge"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type BoxVirtualLedger struct {
	ID            string `json:"id"`
	BoxID         string `json:"box_id"`
	Amount        int64  `json:"amount"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	ReferenceDate int64  `json:"reference_date"`
	CreatedAt     int64  `json:"created_at"`
}

const (
	BoxLedgerTypeRecharge = "RECHARGE"
	BoxLedgerTypeBonus    = "BONUS"
)

type Time = time.Time
