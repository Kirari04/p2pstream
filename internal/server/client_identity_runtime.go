package server

import "net/http"

var directPublicClientIdentityResolver = func() *ClientIdentityResolver {
	resolver, err := NewClientIdentityResolver(nil, ClientIdentityResolverOptions{})
	if err != nil {
		panic(err)
	}
	return resolver
}()

func resolvePublicRequestIdentity(snapshot *publicProxySnapshot, req *http.Request) *http.Request {
	if req == nil {
		return nil
	}
	resolver := directPublicClientIdentityResolver
	if snapshot != nil && snapshot.ClientIdentity != nil {
		resolver = snapshot.ClientIdentity
	}
	if resolver == nil {
		resolver = directPublicClientIdentityResolver
	}
	return resolver.ResolveRequest(req)
}
