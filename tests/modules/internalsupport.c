#include "redismodule.h"

static int internalCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    REDISMODULE_NOT_USED(argv);
    if (argc != 1) return RedisModule_WrongArity(ctx);

    RedisModule_ReplyWithSimpleString(ctx, "OK");
    return REDISMODULE_OK;
}

static int callInternalCommand(RedisModuleCtx *ctx, RedisModuleString **argv, int argc) {
    if (argc > 2) return RedisModule_WrongArity(ctx);

    const char *format = argc == 2 ? "CE" : "E";
    RedisModuleCallReply *reply = RedisModule_Call(ctx, "internalsupport.command", format);
    RedisModule_ReplyWithCallReply(ctx, reply);
    return REDISMODULE_OK;
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
    if (RedisModule_CreateCommand(ctx, "internalsupport.get-secret", getInternalSecretCommand,
                                  "fast", 0, 0, 0) == REDISMODULE_ERR)
        return REDISMODULE_ERR;
    return REDISMODULE_OK;
}
