package handlers

import "github.com/contabase-app/contabase/internal/models"

// testMetasAdminRole is the default actor for the existing Metas handler
// fixtures. Production routes set MetasHandler.ActorRole from authContext;
// tests that exercise an unauthorized actor override it explicitly.
const testMetasAdminRole = models.RoleAdmin
