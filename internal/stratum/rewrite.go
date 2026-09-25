package stratum

import "strings"

// Rewrite is the result of mapping an ASIC login to the pool account.
type Rewrite struct {
	User     string
	Password string
}

// Worker extracts {worker}: the part after the first dot, the whole login
// when there is no dot, "default" when nothing follows the dot.
func Worker(login string) string {
	i := strings.IndexByte(login, '.')
	if i < 0 {
		if login == "" {
			return "default"
		}
		return login
	}
	if w := login[i+1:]; w != "" {
		return w
	}
	return "default"
}

// RewriteLogin maps an ASIC login and password to one pool account.
func RewriteLogin(login, asicPassword, userTemplate, poolPassword string) Rewrite {
	user := strings.NewReplacer("{worker}", Worker(login), "{login}", login).Replace(userTemplate)
	pass := strings.ReplaceAll(poolPassword, "{password}", asicPassword)
	return Rewrite{User: user, Password: pass}
}
