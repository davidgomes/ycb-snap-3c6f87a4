#include "redismodule.h"

static int internalCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    REDISMODULE_NOT_USED(argv);
    if (argc != 1) return RedisModule_WrongArity(ctx);

    RedisModule_ReplyWithSimpleString(ctx, "OK");
    return REDISMODULE_OK;
}

static int callInternalCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    REDISMODULE_NOT_USED(argv);
    if (argc > 2) return RedisModule_WrongArity(ctx);

    const char *format = argc == 2 ? "CE" : "E";
    RedisModuleCallReply *reply = RedisModule_Call(ctx, "internalsupport.command", format);
    RedisModule_ReplyWithCallReply(ctx, reply);
    return REDISMODULE_OK;
}

static int callCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc,
                       int detached, int replicate) {
    if (argc < 2) return RedisModule_WrongArity(ctx);

    RedisModuleCtx *call_ctx = ctx;
    if (detached) {
        call_ctx = RedisModule_GetThreadSafeContext(NULL);
        if (!call_ctx) {
            RedisModule_ReplyWithError(ctx, "ERR failed to create detached context");
            return REDISMODULE_ERR;
        }
    }

    const char *command = RedisModule_StringPtrLen(argv[1], NULL);
    const char *format = detached ? "vCE" : "vE";
    RedisModuleCallReply *reply = RedisModule_Call(call_ctx, command, format,
                                                    argv + 2, argc - 2);
    if (reply) {
        RedisModule_ReplyWithCallReply(ctx, reply);
        RedisModule_FreeCallReply(reply);
        if (replicate)
            RedisModule_ReplicateVerbatim(ctx);
    } else {
        RedisModule_ReplyWithError(ctx, "ERR unknown command");
    }

    if (detached)
        RedisModule_FreeThreadSafeContext(call_ctx);
    return REDISMODULE_OK;
}

static int detachedCallCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    return callCommand(ctx, argv, argc, 1, 0);
}

static int replicatedCallCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    return callCommand(ctx, argv, argc, 0, 1);
}

static int getInternalSecretCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    REDISMODULE_NOT_USED(argv);
    if (argc != 1) return RedisModule_WrongArity(ctx);

    size_t len;
    const char *secret = RedisModule_GetInternalSecret(ctx, &len);
    if (secret)
        RedisModule_ReplyWithStringBuffer(ctx, secret, len);
    else
        RedisModule_ReplyWithNull(ctx);
    return REDISMODULE_OK;
}

int RedisModule_OnLoad(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    REDISMODULE_NOT_USED(argv);
    REDISMODULE_NOT_USED(argc);

    if (RedisModule_Init(ctx, "internalsupport", 1, REDISMODULE_APIVER_1) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    if (RedisModule_CreateCommand(ctx, "internalsupport.command", internalCommand,
                                  "internal fast", 0, 0, 0) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    if (RedisModule_CreateCommand(ctx, "internalsupport.call", callInternalCommand,
                                  "fast", 0, 0, 0) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    if (RedisModule_CreateCommand(ctx, "internalsupport.detached-call", detachedCallCommand,
                                  "fast", 0, 0, 0) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    if (RedisModule_CreateCommand(ctx, "internalsupport.replicated-call", replicatedCallCommand,
                                  "write internal", 0, 0, 0) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    if (RedisModule_CreateCommand(ctx, "internalsupport.get-secret", getInternalSecretCommand,
                                  "fast", 0, 0, 0) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    return REDISMODULE_OK;
}
