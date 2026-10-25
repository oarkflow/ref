package platform

import (
	"fmt"
	"strings"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/chat"
)

// Expression helpers for realtime and chat, usable anywhere an expression or an
// SPL/BCL template is:
//
//	rt_topic("room", input.room_id)      "room:42"
//	rt_user(principal.id)                "user:ada"      (a user's private topic)
//	rt_tenant_topic(tenant, "orders")    "t:acme:orders" (tenant-scoped topic)
//	chat_topic(input.channel)            "chat:ch_9f2…"
//	chat_dm(principal.id, input.peer)    "dm_5be1…"      (same for either order)
//
// They only build names; whether a caller may use a topic is the hub's call.

func init() {
	exprFunctions["rt_topic"] = fnRTTopic
	exprFunctions["rt_user"] = fnRTUser
	exprFunctions["rt_tenant_topic"] = fnRTTenantTopic
	exprFunctions["chat_topic"] = fnChatTopic
	exprFunctions["chat_dm"] = fnChatDM
}

func topicPart(name string, v any) (string, error) {
	s := Stringify(v)
	if s == "" {
		return "", fmt.Errorf("%s: a topic part cannot be empty", name)
	}
	if strings.ContainsAny(s, " \t\r\n\x00") {
		return "", fmt.Errorf("%s: %q contains whitespace", name, s)
	}
	return s, nil
}

var fnRTTopic bcl.EvalFunction = func(args []any, _ *bcl.EvalOptions) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("rt_topic takes a kind and optional parts")
	}
	parts := make([]string, len(args))
	for i, a := range args {
		p, err := topicPart("rt_topic", a)
		if err != nil {
			return nil, err
		}
		parts[i] = p
	}
	return strings.Join(parts, ":"), nil
}

var fnRTUser bcl.EvalFunction = func(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("rt_user", args, 1, 1); err != nil {
		return nil, err
	}
	id, err := topicPart("rt_user", args[0])
	if err != nil {
		return nil, err
	}
	return "user:" + id, nil
}

var fnRTTenantTopic bcl.EvalFunction = func(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("rt_tenant_topic", args, 2, 2); err != nil {
		return nil, err
	}
	tenant, err := topicPart("rt_tenant_topic", args[0])
	if err != nil {
		return nil, err
	}
	name, err := topicPart("rt_tenant_topic", args[1])
	if err != nil {
		return nil, err
	}
	return "t:" + tenant + ":" + name, nil
}

var fnChatTopic bcl.EvalFunction = func(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("chat_topic", args, 1, 1); err != nil {
		return nil, err
	}
	id, err := topicPart("chat_topic", args[0])
	if err != nil {
		return nil, err
	}
	return chat.Topic(id), nil
}

var fnChatDM bcl.EvalFunction = func(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("chat_dm", args, 2, 2); err != nil {
		return nil, err
	}
	a, err := topicPart("chat_dm", args[0])
	if err != nil {
		return nil, err
	}
	b, err := topicPart("chat_dm", args[1])
	if err != nil {
		return nil, err
	}
	return chat.DMID(a, b), nil
}
